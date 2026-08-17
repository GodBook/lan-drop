package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func newTestServer(t *testing.T, pin string) (*Server, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<html>LAN Drop</html>")},
		"app.js":     {Data: []byte("console.log('app');")},
		"style.css":  {Data: []byte("body{}")},
	}
	cfg := NewConfig(18087, "127.0.0.1", dir, pin, fsys)
	srv := NewServer(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Shutdown()
	})
	return srv, ts, dir
}

func TestAuthFlowWithSessionAndRateLimit(t *testing.T) {
	_, ts, _ := newTestServer(t, "1234")
	client := ts.Client()

	// Wrong PIN fails
	res, err := client.Post(ts.URL+"/api/auth", "application/json", strings.NewReader(`{"pin":"0000"}`))
	if err != nil {
		t.Fatalf("auth request failed: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong PIN must 401, got %d", res.StatusCode)
	}

	// Correct PIN mints a session cookie
	res, err = client.Post(ts.URL+"/api/auth", "application/json", strings.NewReader(`{"pin":"1234"}`))
	if err != nil {
		t.Fatalf("auth request failed: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("correct PIN must 200, got %d", res.StatusCode)
	}
	var cookies []string
	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName {
			cookies = append(cookies, c.Value)
		}
	}
	if len(cookies) != 1 || len(cookies[0]) != 64 {
		t.Fatalf("expected 256-bit session cookie, got %v", cookies)
	}

	// Session cookie unlocks protected APIs
	req, _ := http.NewRequest("GET", ts.URL+"/api/files", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookies[0]})
	res, err = client.Do(req)
	if err != nil {
		t.Fatalf("files request failed: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session must authorize /api/files, got %d", res.StatusCode)
	}

	// No credential -> 401
	res, err = client.Get(ts.URL + "/api/files")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing auth must 401, got %d", res.StatusCode)
	}
}

func TestPINQueryRedirectsToCleanURLAndSetsNoReferrer(t *testing.T) {
	_, ts, _ := newTestServer(t, "1234")
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	res, err := client.Get(ts.URL + "/?pin=1234&view=files")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid query PIN must redirect with 303, got %d", res.StatusCode)
	}
	if location := res.Header.Get("Location"); location != "/?view=files" {
		t.Fatalf("redirect must preserve non-secret query only, got %q", location)
	}
	if policy := res.Header.Get("Referrer-Policy"); policy != "no-referrer" {
		t.Fatalf("unexpected referrer policy %q", policy)
	}
	var session *http.Cookie
	for _, cookie := range res.Cookies() {
		if cookie.Name == sessionCookieName {
			session = cookie
		}
	}
	if session == nil || len(session.Value) != 64 {
		t.Fatal("valid query PIN must mint a session before redirect")
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+res.Header.Get("Location"), nil)
	req.AddCookie(session)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clean redirect destination must load with session, got %d", res.StatusCode)
	}

	res, err = client.Get(ts.URL + "/?pin=wrong")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("invalid PIN must be removed and return to login page, got %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	for _, cookie := range res.Cookies() {
		if cookie.Name == sessionCookieName {
			t.Fatal("invalid query PIN must not mint a session")
		}
	}

	res, err = client.Get(ts.URL + "/api/files")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("security header must also be present on API error responses")
	}
}

func TestAuthLockout(t *testing.T) {
	_, ts, _ := newTestServer(t, "1234")
	client := ts.Client()

	// httptest uses a new port per request client? No: same server, RemoteAddr
	// host stays 127.0.0.1 across requests.
	for i := 0; i < authMaxFailures; i++ {
		res, err := client.Post(ts.URL+"/api/auth", "application/json", strings.NewReader(`{"pin":"bad"}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	res, err := client.Post(ts.URL+"/api/auth", "application/json", strings.NewReader(`{"pin":"1234"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected lockout 429 even with correct PIN, got %d", res.StatusCode)
	}
}

func TestConcurrentAuthAttemptsCannotOvershootLockout(t *testing.T) {
	_, ts, _ := newTestServer(t, "1234")
	const attempts = 32
	start := make(chan struct{})
	statuses := make(chan int, attempts)
	var workers sync.WaitGroup

	for i := 0; i < attempts; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			res, err := ts.Client().Post(ts.URL+"/api/auth", "application/json", strings.NewReader(`{"pin":"wrong"}`))
			if err != nil {
				statuses <- 0
				return
			}
			res.Body.Close()
			statuses <- res.StatusCode
		}()
	}
	close(start)
	workers.Wait()
	close(statuses)

	unauthorized := 0
	locked := 0
	for status := range statuses {
		switch status {
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusTooManyRequests:
			locked++
		default:
			t.Fatalf("unexpected concurrent auth response status %d", status)
		}
	}
	if unauthorized != authMaxFailures-1 || locked != attempts-(authMaxFailures-1) {
		t.Fatalf("lockout admitted %d failures and locked %d requests; want %d and %d", unauthorized, locked, authMaxFailures-1, attempts-(authMaxFailures-1))
	}
}

func TestDirectPINCredentialsShareAuthLockout(t *testing.T) {
	_, ts, _ := newTestServer(t, "1234")
	for i := 0; i < authMaxFailures; i++ {
		res, err := http.Get(ts.URL + "/api/files?pin=wrong")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}

	res, err := http.Get(ts.URL + "/api/files?pin=1234")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("direct query PIN must honor shared lockout, got %d", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/files", nil)
	req.Header.Set("X-PIN", "1234")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("X-PIN must honor shared lockout, got %d", res.StatusCode)
	}

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err = client.Get(ts.URL + "/?pin=1234")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for _, cookie := range res.Cookies() {
		if cookie.Name == sessionCookieName {
			t.Fatal("root query PIN must not bypass the shared lockout")
		}
	}
}

func TestConnectQRIsProtectedAndContainsLANURL(t *testing.T) {
	_, ts, _ := newTestServer(t, "1234")

	res, err := http.Get(ts.URL + "/api/qr")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("QR endpoint without credentials must 401, got %d", res.StatusCode)
	}

	res, err = http.Get(ts.URL + "/api/qr?pin=1234")
	if err != nil {
		t.Fatal(err)
	}
	svg, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("authorized QR request failed: %d", res.StatusCode)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "image/svg+xml") || !bytes.Contains(svg, []byte("<svg")) {
		t.Fatal("QR endpoint must return an SVG image")
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("QR response must not be cached")
	}

	res, err = http.Get(ts.URL + "/api/qr?format=json&pin=1234")
	if err != nil {
		t.Fatal(err)
	}
	var details struct {
		URL string `json:"url"`
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(res.Body).Decode(&details); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if details.URL != "http://127.0.0.1:18087/?pin=1234" || details.PIN != "1234" {
		t.Fatalf("unexpected QR details: %+v", details)
	}
}

func uploadChunkResponseE(url, fileID, fileName string, index, total int, fileSize int64, data []byte) (*http.Response, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("file_id", fileID)
	_ = mw.WriteField("chunk_index", fmt.Sprint(index))
	_ = mw.WriteField("total_chunks", fmt.Sprint(total))
	_ = mw.WriteField("filename", fileName)
	_ = mw.WriteField("file_size", fmt.Sprint(fileSize))
	fw, _ := mw.CreateFormFile("chunk", "blob")
	_, _ = fw.Write(data)
	_ = mw.Close()

	return http.Post(url+"/api/upload/chunk", mw.FormDataContentType(), &buf)
}

func uploadChunkResponse(t *testing.T, url, fileID, fileName string, index, total int, fileSize int64, data []byte) *http.Response {
	t.Helper()
	res, err := uploadChunkResponseE(url, fileID, fileName, index, total, fileSize, data)
	if err != nil {
		t.Fatalf("chunk upload failed: %v", err)
	}
	return res
}

func uploadChunk(t *testing.T, url, fileID, fileName string, index, total int, fileSize int64, data []byte) {
	t.Helper()
	res := uploadChunkResponse(t, url, fileID, fileName, index, total, fileSize, data)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("chunk upload status %d: %s", res.StatusCode, body)
	}
}

func TestCancelUploadRemovesTemporaryChunks(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	uploadChunk(t, ts.URL, "cancel123", "cancel.bin", 0, 2, 8, []byte("part"))

	payload := strings.NewReader(`{"file_id":"cancel123"}`)
	res, err := http.Post(ts.URL+"/api/upload/cancel", "application/json", payload)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp_cancel123")); !os.IsNotExist(err) {
		t.Fatal("cancel must remove the temporary upload directory")
	}
}

func completeUploadResponse(t *testing.T, url, fileID, fileName string, total int, fileSize interface{}) *http.Response {
	t.Helper()
	payload := map[string]interface{}{
		"file_id":      fileID,
		"filename":     fileName,
		"total_chunks": total,
	}
	if fileSize != nil {
		payload["file_size"] = fileSize
	}
	body, _ := json.Marshal(payload)
	res, err := http.Post(url+"/api/upload/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("complete upload failed: %v", err)
	}
	return res
}

func TestUploadRejectsOversizedChunkAndRequestBody(t *testing.T) {
	_, ts, _ := newTestServer(t, "")
	oversizedChunk := bytes.Repeat([]byte("x"), int(maxChunkSize)+1)
	res := uploadChunkResponse(t, ts.URL, "oversizedchunk", "large.bin", 0, 2, int64(len(oversizedChunk)), oversizedChunk)
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunk over 5 MiB must return 413, got %d", res.StatusCode)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("file_id", "oversizedbody")
	_ = mw.WriteField("chunk_index", "0")
	_ = mw.WriteField("total_chunks", "1")
	_ = mw.WriteField("filename", "body.bin")
	_ = mw.WriteField("file_size", fmt.Sprint(maxChunkSize))
	_ = mw.WriteField("padding", strings.Repeat("p", int(maxMultipartOverhead)+64<<10))
	fw, _ := mw.CreateFormFile("chunk", "blob")
	_, _ = fw.Write(bytes.Repeat([]byte("a"), int(maxChunkSize)))
	_ = mw.Close()
	res, err := http.Post(ts.URL+"/api/upload/chunk", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("multipart request over hard body cap must return 413, got %d", res.StatusCode)
	}
}

type readTrackingBody struct {
	read bool
}

func (b *readTrackingBody) Read(_ []byte) (int, error) {
	b.read = true
	return 0, io.EOF
}

func (b *readTrackingBody) Close() error { return nil }

func TestUploadChunkAdmissionRejectsBeforeReadingBody(t *testing.T) {
	srv, ts, _ := newTestServer(t, "")
	ts.Close()
	for i := 0; i < cap(srv.chunkRequestSlots); i++ {
		srv.chunkRequestSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(srv.chunkRequestSlots); i++ {
			<-srv.chunkRequestSlots
		}
	}()

	body := &readTrackingBody{}
	req := httptest.NewRequest(http.MethodPost, "/api/upload/chunk", nil)
	req.Body = body
	recorder := httptest.NewRecorder()
	srv.handleUploadChunk(recorder, req)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("saturated chunk admission must return 429, got %d", recorder.Code)
	}
	if body.read {
		t.Fatal("rejected chunk request body must not be read")
	}
	if retryAfter := recorder.Header().Get("Retry-After"); retryAfter == "" {
		t.Fatal("429 response must tell clients when to retry")
	}
}

func TestUploadValidatesIndexLimitsAndMetadata(t *testing.T) {
	_, ts, _ := newTestServer(t, "")

	res := uploadChunkResponse(t, ts.URL, "badindex", "index.bin", 1, 1, 1, []byte("x"))
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("chunk_index >= total_chunks must return 400, got %d", res.StatusCode)
	}

	res = uploadChunkResponse(t, ts.URL, "toomanychunks", "count.bin", 0, maxUploadChunks+1, 1, []byte("x"))
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("too many chunks must return 400, got %d", res.StatusCode)
	}

	res = uploadChunkResponse(t, ts.URL, "toolargefile", "size.bin", 0, maxUploadChunks, maxUploadSize+1, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("file over total size cap must return 400, got %d", res.StatusCode)
	}

	uploadChunk(t, ts.URL, "metaconflict", "first.bin", 0, 2, 2, []byte("a"))
	res = uploadChunkResponse(t, ts.URL, "metaconflict", "second.bin", 1, 2, 2, []byte("b"))
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("same file_id with changed metadata must return 409, got %d", res.StatusCode)
	}
}

func TestUploadDuplicateIsIdempotentButConflictingDataIsRejected(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	uploadChunk(t, ts.URL, "duplicate1", "duplicate.bin", 0, 1, 4, []byte("same"))

	res := uploadChunkResponse(t, ts.URL, "duplicate1", "duplicate.bin", 0, 1, 4, []byte("same"))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("identical retry must be idempotent, got %d", res.StatusCode)
	}
	res = uploadChunkResponse(t, ts.URL, "duplicate1", "duplicate.bin", 0, 1, 4, []byte("evil"))
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("different data for existing chunk index must return 409, got %d", res.StatusCode)
	}

	res = completeUploadResponse(t, ts.URL, "duplicate1", "duplicate.bin", 1, int64(4))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("completion after duplicate retry failed: %d", res.StatusCode)
	}
	data, err := os.ReadFile(filepath.Join(dir, "duplicate.bin"))
	if err != nil || string(data) != "same" {
		t.Fatalf("duplicate conflict must not replace stored data: %q, %v", data, err)
	}
}

func TestConcurrentChunksCannotCorruptUploadState(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	type result struct {
		status int
		err    error
	}
	postAsync := func(results chan<- result, fileID string, index, total int, size int64, data string) {
		res, err := uploadChunkResponseE(ts.URL, fileID, "concurrent.bin", index, total, size, []byte(data))
		if err != nil {
			results <- result{err: err}
			return
		}
		_ = res.Body.Close()
		results <- result{status: res.StatusCode}
	}

	// Two writers racing for one index cannot replace one another.
	results := make(chan result, 2)
	go postAsync(results, "sameindex", 0, 1, 4, "AAAA")
	go postAsync(results, "sameindex", 0, 1, 4, "BBBB")
	statusCounts := map[int]int{}
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		statusCounts[got.status]++
	}
	if statusCounts[http.StatusOK] != 1 || statusCounts[http.StatusConflict] != 1 {
		t.Fatalf("same-index race must yield one 200 and one 409, got %+v", statusCounts)
	}
	res := completeUploadResponse(t, ts.URL, "sameindex", "concurrent.bin", 1, int64(4))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("same-index winner must remain completable, got %d", res.StatusCode)
	}
	winner, err := os.ReadFile(filepath.Join(dir, "concurrent.bin"))
	if err != nil || (string(winner) != "AAAA" && string(winner) != "BBBB") {
		t.Fatalf("unexpected race winner data %q: %v", winner, err)
	}

	// Separate indices may arrive concurrently and still merge in index order.
	results = make(chan result, 2)
	go postAsync(results, "twoindices", 0, 2, 8, "1111")
	go postAsync(results, "twoindices", 1, 2, 8, "2222")
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.status != http.StatusOK {
			t.Fatalf("different-index concurrent upload failed with %d", got.status)
		}
	}
	res = completeUploadResponse(t, ts.URL, "twoindices", "concurrent.bin", 2, int64(8))
	var complete struct {
		File FileItem `json:"file"`
	}
	if err := json.NewDecoder(res.Body).Decode(&complete); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("different-index completion failed with %d", res.StatusCode)
	}
	merged, err := os.ReadFile(filepath.Join(dir, complete.File.Name))
	if err != nil || string(merged) != "11112222" {
		t.Fatalf("concurrent chunks merged incorrectly: %q, %v", merged, err)
	}
}

func TestCompleteRequiresAndVerifiesDeclaredFileSize(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	uploadChunk(t, ts.URL, "wrongsize", "wrong.bin", 0, 1, 5, []byte("four"))

	res := completeUploadResponse(t, ts.URL, "wrongsize", "wrong.bin", 1, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("completion without file_size must return 400, got %d", res.StatusCode)
	}

	res = completeUploadResponse(t, ts.URL, "wrongsize", "wrong.bin", 1, int64(4))
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("completion metadata mismatch must return 409, got %d", res.StatusCode)
	}

	res = completeUploadResponse(t, ts.URL, "wrongsize", "wrong.bin", 1, int64(5))
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("actual chunk total mismatch must return 409, got %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "wrong.bin")); !os.IsNotExist(err) {
		t.Fatal("size mismatch must not publish a final file")
	}
}

func TestUploadReturns507WhenDiskReserveWouldBeViolated(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	originalProbe := diskAvailableBytes
	diskAvailableBytes = func(string) (uint64, error) {
		return uint64(minFreeDiskReserve), nil
	}
	t.Cleanup(func() { diskAvailableBytes = originalProbe })

	res := uploadChunkResponse(t, ts.URL, "nodiskspace", "full.bin", 0, 1, 1, []byte("x"))
	res.Body.Close()
	if res.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("insufficient remaining disk must return 507, got %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp_nodiskspace", "chunk_00000")); !os.IsNotExist(err) {
		t.Fatal("507 response must not publish a chunk")
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp_nodiskspace")); !os.IsNotExist(err) {
		t.Fatal("507 on the first chunk must not leave upload metadata behind")
	}
}

func TestCompleteReturns507WithoutDestroyingResumableChunks(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	uploadChunk(t, ts.URL, "completefull", "complete.bin", 0, 1, 4, []byte("data"))

	originalProbe := diskAvailableBytes
	diskAvailableBytes = func(string) (uint64, error) {
		return uint64(minFreeDiskReserve), nil
	}
	t.Cleanup(func() { diskAvailableBytes = originalProbe })

	res := completeUploadResponse(t, ts.URL, "completefull", "complete.bin", 1, int64(4))
	res.Body.Close()
	if res.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("completion without merge headroom must return 507, got %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "complete.bin")); !os.IsNotExist(err) {
		t.Fatal("507 completion must not publish a final file")
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp_completefull", "chunk_00000")); err != nil {
		t.Fatal("507 completion must preserve chunks for retry")
	}
}

func TestStorageReservationsAccountForConcurrentWriters(t *testing.T) {
	srv, _, dir := newTestServer(t, "")
	originalProbe := diskAvailableBytes
	diskAvailableBytes = func(string) (uint64, error) {
		return uint64(minFreeDiskReserve + 10), nil
	}
	t.Cleanup(func() { diskAvailableBytes = originalProbe })

	releaseFirst, ok, err := srv.reserveStorage(dir, 6)
	if err != nil || !ok {
		t.Fatalf("first storage reservation failed: ok=%v err=%v", ok, err)
	}
	if _, ok, err := srv.reserveStorage(dir, 5); err != nil || ok {
		t.Fatalf("overcommitted reservation must be rejected: ok=%v err=%v", ok, err)
	}
	releaseFirst()
	releaseSecond, ok, err := srv.reserveStorage(dir, 5)
	if err != nil || !ok {
		t.Fatalf("released storage must become reservable: ok=%v err=%v", ok, err)
	}
	releaseSecond()
}

func TestUploadRejectsUnsafeFilenamesAndLimitsActiveStates(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	for _, fileName := range []string{
		strings.Repeat("x", maxClientFilenameBytes+1),
		".landrop_feed.json",
		"line\nbreak.txt",
		"reserved?.txt",
		"CON.txt",
		"COM1.foo.bar",
	} {
		res := uploadChunkResponse(t, ts.URL, "unsafe"+fmt.Sprint(len(fileName)), fileName, 0, 1, 1, []byte("x"))
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("unsafe filename %q must return 400, got %d", fileName, res.StatusCode)
		}
	}

	for i := 0; i < maxActiveUploads; i++ {
		name := fmt.Sprintf(".tmp_active%03d", i)
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	res := uploadChunkResponse(t, ts.URL, "oneuploadtoomany", "valid.bin", 0, 1, 1, []byte("x"))
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("active upload state limit must return 429, got %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp_oneuploadtoomany")); !os.IsNotExist(err) {
		t.Fatal("rejected active upload must not leave a state directory")
	}
}

func TestUploadStaysBoundToInitialDirectory(t *testing.T) {
	srv, ts, firstDir := newTestServer(t, "")
	uploadChunk(t, ts.URL, "directorybound", "bound.bin", 0, 2, 8, []byte("1111"))

	secondDir := t.TempDir()
	if err := srv.cfg.SetUploadDir(secondDir); err != nil {
		t.Fatal(err)
	}
	uploadChunk(t, ts.URL, "directorybound", "bound.bin", 1, 2, 8, []byte("2222"))

	res := completeUploadResponse(t, ts.URL, "directorybound", "bound.bin", 2, int64(8))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("directory-bound completion failed with %d", res.StatusCode)
	}
	data, err := os.ReadFile(filepath.Join(firstDir, "bound.bin"))
	if err != nil || string(data) != "11112222" {
		t.Fatalf("upload was split across storage directories: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(secondDir, "bound.bin")); !os.IsNotExist(err) {
		t.Fatal("in-flight upload must not move to a newly selected directory")
	}
}

func TestCancelTombstoneRejectsQueuedChunks(t *testing.T) {
	srv, ts, dir := newTestServer(t, "")
	uploadChunk(t, ts.URL, "cancelrace", "cancel.bin", 0, 2, 8, []byte("1111"))
	type requestResult struct {
		status int
		err    error
	}

	unlock := srv.lockUpload("cancelrace")
	cancelResult := make(chan requestResult, 1)
	go func() {
		res, err := http.Post(ts.URL+"/api/upload/cancel", "application/json", strings.NewReader(`{"file_id":"cancelrace"}`))
		if err != nil {
			cancelResult <- requestResult{err: err}
			return
		}
		res.Body.Close()
		cancelResult <- requestResult{status: res.StatusCode}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !srv.uploadIsCancelled("cancelrace") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !srv.uploadIsCancelled("cancelrace") {
		unlock()
		t.Fatal("cancel request did not install its tombstone")
	}

	chunkResult := make(chan requestResult, 1)
	go func() {
		res, err := uploadChunkResponseE(ts.URL, "cancelrace", "cancel.bin", 1, 2, 8, []byte("2222"))
		if err != nil {
			chunkResult <- requestResult{err: err}
			return
		}
		res.Body.Close()
		chunkResult <- requestResult{status: res.StatusCode}
	}()
	unlock()

	if got := <-cancelResult; got.err != nil || got.status != http.StatusOK {
		t.Fatalf("cancel request failed: %+v", got)
	}
	if got := <-chunkResult; got.err != nil || got.status != http.StatusConflict {
		t.Fatalf("queued chunk must be rejected after cancel: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp_cancelrace")); !os.IsNotExist(err) {
		t.Fatal("queued chunk recreated state after cancel returned")
	}
}

func TestUploadResumeMergeDownloadWithSpecialName(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	// disable PIN semantics for direct calls
	url := ts.URL

	fileID := "testid123"
	contentA := bytes.Repeat([]byte("A"), 100)
	contentB := bytes.Repeat([]byte("B"), 50)
	fileSize := int64(len(contentA) + len(contentB))
	uploadChunk(t, url, fileID, "test 文件 #1.bin", 0, 2, fileSize, contentA)
	uploadChunk(t, url, fileID, "test 文件 #1.bin", 1, 2, fileSize, contentB)

	// Status endpoint sees both chunks
	res, err := http.Get(url + "/api/upload/status?file_id=" + fileID)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Chunks []int `json:"chunks"`
	}
	_ = json.NewDecoder(res.Body).Decode(&status)
	res.Body.Close()
	if len(status.Chunks) != 2 || status.Chunks[0] != 0 || status.Chunks[1] != 1 {
		t.Fatalf("status must report chunks [0 1], got %v", status.Chunks)
	}

	// Merge
	body, _ := json.Marshal(map[string]interface{}{
		"file_id":      fileID,
		"filename":     "test 文件 #1.bin",
		"total_chunks": 2,
		"file_size":    fileSize,
	})
	res, err = http.Post(url+"/api/upload/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var complete struct {
		File FileItem `json:"file"`
	}
	_ = json.NewDecoder(res.Body).Decode(&complete)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("complete failed: %d", res.StatusCode)
	}
	if complete.File.Name != "test 文件 #1.bin" {
		t.Fatalf("unexpected final name: %q", complete.File.Name)
	}
	if !strings.Contains(complete.File.URL, "%") {
		t.Fatalf("URL must be escaped for special chars: %q", complete.File.URL)
	}

	// Temp dir cleaned after merge
	if _, err := os.Stat(filepath.Join(dir, ".tmp_"+fileID)); !os.IsNotExist(err) {
		t.Fatal("temp chunk dir must be removed after merge")
	}

	// Download via the escaped URL; content must round-trip
	res, err = http.Get(url + complete.File.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("download failed: %d", res.StatusCode)
	}
	if !bytes.Equal(got, append(contentA, contentB...)) {
		t.Fatalf("downloaded content mismatch: %d bytes", len(got))
	}

	// Content-Disposition must carry RFC 5987 encoded name and inline mode
	res, err = http.Get(url + complete.File.URL + "?inline=1")
	if err != nil {
		t.Fatal(err)
	}
	cd := res.Header.Get("Content-Disposition")
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if !strings.Contains(cd, "inline") || !strings.Contains(cd, "UTF-8''") {
		t.Fatalf("inline disposition with RFC5987 name expected, got %q", cd)
	}
}

func TestCompleteWithMissingChunksLeavesNoPartialFile(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	uploadChunk(t, ts.URL, "partial1", "partial.bin", 0, 3, 30, []byte("only-first"))

	body, _ := json.Marshal(map[string]interface{}{
		"file_id":      "partial1",
		"filename":     "partial.bin",
		"total_chunks": 3,
		"file_size":    30,
	})
	res, err := http.Post(ts.URL+"/api/upload/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing chunks must 400, got %d", res.StatusCode)
	}
	for _, name := range []string{"partial.bin", "partial.bin.part"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s must not exist after failed merge", name)
		}
	}
}

func TestFileIDValidation(t *testing.T) {
	_, ts, _ := newTestServer(t, "")
	// path-traversal style ids must be rejected
	for _, badID := range []string{"../evil", "a/b", "..", "id with space"} {
		res, err := http.Get(ts.URL + "/api/upload/status?file_id=" + badID)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("file_id %q must be rejected, got %d", badID, res.StatusCode)
		}
	}
}

func TestCleanupTempChunks(t *testing.T) {
	srv, _, dir := newTestServer(t, "")
	oldDir := filepath.Join(dir, ".tmp_oldabandoned")
	newDir := filepath.Join(dir, ".tmp_freshupload")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Age the first dir beyond the cutoff
	past := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(oldDir, past, past); err != nil {
		t.Fatal(err)
	}

	srv.CleanupTempChunks(2 * time.Hour)

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("stale temp dir must be cleaned")
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatal("fresh temp dir must survive")
	}
}

func TestFileListSearchTypeAndPagination(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	fileNames := []string{
		"photo-old.jpg",
		"photo-new.png",
		"song.mp3",
		"movie.mp4",
		"Report-Q3.pdf",
		"bundle.zip",
		"raw.bin",
	}
	baseTime := time.Now().Add(-time.Hour)
	for index, name := range fileNames {
		fullPath := filepath.Join(dir, name)
		if err := os.WriteFile(fullPath, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		modTime := baseTime.Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(fullPath, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}

	decodeList := func(endpoint string) struct {
		Files      []FileItem `json:"files"`
		Total      int        `json:"total"`
		Page       int        `json:"page"`
		PageSize   int        `json:"page_size"`
		TotalPages int        `json:"total_pages"`
	} {
		t.Helper()
		res, err := http.Get(ts.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list request %s failed: %d", endpoint, res.StatusCode)
		}
		var payload struct {
			Files      []FileItem `json:"files"`
			Total      int        `json:"total"`
			Page       int        `json:"page"`
			PageSize   int        `json:"page_size"`
			TotalPages int        `json:"total_pages"`
		}
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	images := decodeList("/api/files?type=image&page=1&page_size=1")
	if images.Total != 2 || len(images.Files) != 1 || images.Files[0].Name != "photo-new.png" || images.TotalPages != 2 {
		t.Fatalf("unexpected paged image list: %+v", images)
	}
	documents := decodeList("/api/files?q=report&type=document&page=1&page_size=20")
	if documents.Total != 1 || len(documents.Files) != 1 || documents.Files[0].Name != "Report-Q3.pdf" {
		t.Fatalf("case-insensitive document search failed: %+v", documents)
	}
	other := decodeList("/api/files?type=other&page=1&page_size=20")
	if other.Total != 1 || len(other.Files) != 1 || other.Files[0].Name != "raw.bin" {
		t.Fatalf("other type filter failed: %+v", other)
	}
	legacy := decodeList("/api/files")
	if legacy.Total != len(fileNames) || len(legacy.Files) != len(fileNames) || legacy.PageSize != len(fileNames) {
		t.Fatalf("unparameterized list must preserve full-list behavior: %+v", legacy)
	}
	if err := os.WriteFile(filepath.Join(dir, "new-after-cache.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(dir, future, future); err != nil {
		t.Fatal(err)
	}
	refreshed := decodeList("/api/files")
	if refreshed.Total != len(fileNames)+1 {
		t.Fatalf("directory change must invalidate the cached file index: %+v", refreshed)
	}

	res, err := http.Get(ts.URL + "/api/files?page=0&page_size=20")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid pagination must return 400, got %d", res.StatusCode)
	}
}

func TestBatchDeleteAndSingleDeleteCompatibility(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	for _, name := range []string{"first.txt", "second.txt", "third.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}

	body, _ := json.Marshal(map[string]interface{}{
		"filenames": []string{"first.txt", "second.txt", "missing.txt", "first.txt"},
	})
	res, err := http.Post(ts.URL+"/api/files/delete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var batch struct {
		Deleted []string `json:"deleted"`
	}
	if err := json.NewDecoder(res.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || len(batch.Deleted) != 2 {
		t.Fatalf("unexpected batch delete result: %d %+v", res.StatusCode, batch.Deleted)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s must be deleted", name)
		}
	}

	body, _ = json.Marshal(map[string]interface{}{"filename": "third.txt"})
	res, err = http.Post(ts.URL+"/api/files/delete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("legacy single delete must remain supported, got %d", res.StatusCode)
	}

	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(map[string]interface{}{"filename": "../keep.txt"})
	res, err = http.Post(ts.URL+"/api/files/delete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("path-like delete target must return 400, got %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatal("invalid batch must not delete sanitized path target")
	}
}

func TestInternalFilesAndDirectoriesCannotBeDownloadedOrDeleted(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	internalPath := filepath.Join(dir, feedStoreFileName)
	if err := os.WriteFile(internalPath, []byte("private state"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, endpoint := range []string{
		"/api/download/",
		"/api/download/" + feedStoreFileName,
	} {
		res, err := http.Get(ts.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("internal download %q must return 404, got %d and %q", endpoint, res.StatusCode, body)
		}
	}

	body, _ := json.Marshal(map[string]interface{}{"filename": feedStoreFileName})
	res, err := http.Post(ts.URL+"/api/files/delete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("internal file deletion must return 400, got %d", res.StatusCode)
	}
	if data, err := os.ReadFile(internalPath); err != nil || string(data) != "private state" {
		t.Fatalf("internal file must remain intact: %q, %v", data, err)
	}
}

func TestTextSendAndFeed(t *testing.T) {
	_, ts, _ := newTestServer(t, "")

	res, err := http.Post(ts.URL+"/api/text/send", "application/json",
		strings.NewReader(`{"content":"hello integration","sender":"tester"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("text send failed: %d", res.StatusCode)
	}

	res, err = http.Get(ts.URL + "/api/text/feed")
	if err != nil {
		t.Fatal(err)
	}
	var feed struct {
		Feed []TextMessage `json:"feed"`
	}
	_ = json.NewDecoder(res.Body).Decode(&feed)
	res.Body.Close()
	if len(feed.Feed) != 1 || feed.Feed[0].Content != "hello integration" {
		t.Fatalf("feed mismatch: %+v", feed.Feed)
	}
}

func TestStaticETagCaching(t *testing.T) {
	_, ts, _ := newTestServer(t, "")

	res, err := http.Get(ts.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	etag := res.Header.Get("ETag")
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if etag == "" {
		t.Fatal("static assets must carry an ETag")
	}

	req, _ := http.NewRequest("GET", ts.URL+"/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("matching If-None-Match must yield 304, got %d", res.StatusCode)
	}
}

func TestWebSocketOriginCheck(t *testing.T) {
	_, ts, _ := newTestServer(t, "")

	// Foreign origin must be refused
	req, _ := http.NewRequest("GET", ts.URL+"/api/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Origin", "http://evil.example.com")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin must be 403, got %d", res.StatusCode)
	}
}
