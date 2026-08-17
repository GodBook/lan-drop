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

func uploadChunk(t *testing.T, ts *httptest.Server, url, fileID string, index, total int, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("file_id", fileID)
	_ = mw.WriteField("chunk_index", fmt.Sprint(index))
	_ = mw.WriteField("total_chunks", fmt.Sprint(total))
	_ = mw.WriteField("filename", "test 文件 #1.bin")
	fw, _ := mw.CreateFormFile("chunk", "blob")
	_, _ = fw.Write(data)
	_ = mw.Close()

	res, err := http.Post(url+"/api/upload/chunk", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatalf("chunk upload failed: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("chunk upload status %d: %s", res.StatusCode, body)
	}
}

func TestUploadResumeMergeDownloadWithSpecialName(t *testing.T) {
	_, ts, dir := newTestServer(t, "")
	// disable PIN semantics for direct calls
	url := ts.URL

	fileID := "testid123"
	contentA := bytes.Repeat([]byte("A"), 100)
	contentB := bytes.Repeat([]byte("B"), 50)
	uploadChunk(t, nil, url, fileID, 0, 2, contentA)
	uploadChunk(t, nil, url, fileID, 1, 2, contentB)

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
	uploadChunk(t, nil, ts.URL, "partial1", 0, 3, []byte("only-first"))

	body, _ := json.Marshal(map[string]interface{}{
		"file_id":      "partial1",
		"filename":     "partial.bin",
		"total_chunks": 3,
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
