package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxChunkSize                 int64 = 5 << 20
	maxMultipartOverhead         int64 = 512 << 10
	maxChunkRequestSize                = maxChunkSize + maxMultipartOverhead
	maxMultipartMemory           int64 = 256 << 10
	maxConcurrentChunkRequests         = 16
	maxUploadChunks                    = 4096
	maxUploadSize                int64 = 20 << 30
	minFreeDiskReserve           int64 = 64 << 20
	maxDeleteFiles                     = 100
	maxFileListPageSize                = 200
	defaultFileListPageSize            = 20
	maxClientFilenameBytes             = 240
	maxActiveUploads                   = 128
	maxCancelledUploadTombstones       = 1024
	uploadMetadataFile                 = ".upload.json"
	cancelTombstoneTTL                 = 5 * time.Minute
)

// ChunkUploadRequest documents the chunk metadata accepted by the upload API.
// Multipart requests carry these values as form fields rather than JSON.
type ChunkUploadRequest struct {
	FileID      string `json:"file_id"`
	ChunkIndex  int    `json:"chunk_index"`
	TotalChunks int    `json:"total_chunks"`
	FileName    string `json:"filename"`
	FileSize    int64  `json:"file_size"`
}

type uploadMetadata struct {
	FileID      string `json:"file_id"`
	TotalChunks int    `json:"total_chunks"`
	FileName    string `json:"filename"`
	FileSize    int64  `json:"file_size"`
}

type uploadLock struct {
	mu   sync.Mutex
	refs int
}

type fileIndexCache struct {
	directoryModTime time.Time
	files            []FileItem
}

// validFileID guards the temp-directory naming against path tricks.
var fileIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var chunkFilePattern = regexp.MustCompile(`^chunk_([0-9]{5})$`)
var errUploadCancelled = errors.New("upload cancelled")

// Indirection keeps the filesystem probe testable without platform-specific
// fixtures. Production always uses availableDiskBytes from disk_space_*.go.
var diskAvailableBytes = availableDiskBytes

func validFileID(id string) bool {
	return fileIDPattern.MatchString(id)
}

func cleanClientFilename(name string) (string, bool) {
	if name == "" || len(name) > maxClientFilenameBytes || !utf8.ValidString(name) || strings.ContainsAny(name, `/\\<>:"|?*`) ||
		strings.HasPrefix(name, ".") || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") ||
		strings.HasSuffix(strings.ToLower(name), ".part") {
		return "", false
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return "", false
		}
	}
	clean := filepath.Base(name)
	if clean != name || clean == "." || clean == ".." || windowsReservedFilename(clean) {
		return "", false
	}
	return clean, true
}

func windowsReservedFilename(name string) bool {
	base, _, _ := strings.Cut(strings.ToUpper(name), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

func validateUploadMetadata(fileID, fileName string, totalChunks int, fileSize int64) (uploadMetadata, error) {
	if !validFileID(fileID) {
		return uploadMetadata{}, errors.New("invalid file_id")
	}
	cleanName, ok := cleanClientFilename(fileName)
	if !ok {
		return uploadMetadata{}, errors.New("invalid filename")
	}
	if totalChunks < 1 || totalChunks > maxUploadChunks {
		return uploadMetadata{}, errors.New("invalid total_chunks")
	}
	if fileSize < 0 || fileSize > maxUploadSize {
		return uploadMetadata{}, errors.New("invalid file_size")
	}
	if fileSize > int64(totalChunks)*maxChunkSize || (fileSize == 0 && totalChunks != 1) {
		return uploadMetadata{}, errors.New("file_size does not fit total_chunks")
	}
	return uploadMetadata{
		FileID:      fileID,
		TotalChunks: totalChunks,
		FileName:    cleanName,
		FileSize:    fileSize,
	}, nil
}

func (s *Server) lockUpload(fileID string) func() {
	s.uploadMu.Lock()
	if s.uploadLocks == nil {
		s.uploadLocks = make(map[string]*uploadLock)
	}
	lock := s.uploadLocks[fileID]
	if lock == nil {
		lock = &uploadLock{}
		s.uploadLocks[fileID] = lock
	}
	lock.refs++
	s.uploadMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.uploadMu.Lock()
		lock.refs--
		if lock.refs == 0 && s.uploadLocks[fileID] == lock {
			delete(s.uploadLocks, fileID)
		}
		s.uploadMu.Unlock()
	}
}

func (s *Server) bindUploadDirectory(fileID, directory string) string {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	if s.uploadDirectories == nil {
		s.uploadDirectories = make(map[string]string)
	}
	if existing := s.uploadDirectories[fileID]; existing != "" {
		return existing
	}
	s.uploadDirectories[fileID] = directory
	return directory
}

func (s *Server) uploadDirectoryFor(fileID, fallback string) string {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	if existing := s.uploadDirectories[fileID]; existing != "" {
		return existing
	}
	return fallback
}

func (s *Server) forgetUploadDirectory(fileID, directory string) {
	s.uploadMu.Lock()
	if s.uploadDirectories[fileID] == directory {
		delete(s.uploadDirectories, fileID)
	}
	s.uploadMu.Unlock()
}

func (s *Server) uploadDirectorySnapshot(current string) []string {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	directories := []string{current}
	seen := map[string]struct{}{current: {}}
	for _, directory := range s.uploadDirectories {
		if _, exists := seen[directory]; exists {
			continue
		}
		seen[directory] = struct{}{}
		directories = append(directories, directory)
	}
	return directories
}

func (s *Server) markUploadCancelled(fileID string) {
	now := time.Now()
	s.uploadMu.Lock()
	if s.cancelledUploads == nil {
		s.cancelledUploads = make(map[string]time.Time)
	}
	for id, expiry := range s.cancelledUploads {
		if !now.Before(expiry) {
			delete(s.cancelledUploads, id)
		}
	}
	if len(s.cancelledUploads) >= maxCancelledUploadTombstones {
		var oldestID string
		var oldestExpiry time.Time
		for id, expiry := range s.cancelledUploads {
			if oldestID == "" || expiry.Before(oldestExpiry) {
				oldestID = id
				oldestExpiry = expiry
			}
		}
		delete(s.cancelledUploads, oldestID)
	}
	s.cancelledUploads[fileID] = now.Add(cancelTombstoneTTL)
	s.uploadMu.Unlock()
}

func (s *Server) uploadIsCancelled(fileID string) bool {
	now := time.Now()
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	expiry, exists := s.cancelledUploads[fileID]
	if exists && !now.Before(expiry) {
		delete(s.cancelledUploads, fileID)
		return false
	}
	return exists
}

func (s *Server) publishUploadFile(fileID, partPath, targetPath string) (string, error) {
	// Hold the cancellation registry lock through publication so cancel and
	// final rename have a single, deterministic ordering.
	s.uploadMu.Lock()
	expiry, cancelled := s.cancelledUploads[fileID]
	if cancelled && !time.Now().Before(expiry) {
		delete(s.cancelledUploads, fileID)
		cancelled = false
	}
	if cancelled {
		s.uploadMu.Unlock()
		return "", errUploadCancelled
	}

	s.finalizeMu.Lock()
	finalPath := getUniqueFilePath(targetPath)
	err := os.Rename(partPath, finalPath)
	s.finalizeMu.Unlock()
	s.uploadMu.Unlock()
	if err != nil {
		return "", err
	}
	return finalPath, nil
}

func readUploadMetadata(tempDir string) (uploadMetadata, error) {
	data, err := os.ReadFile(filepath.Join(tempDir, uploadMetadataFile))
	if err != nil {
		return uploadMetadata{}, err
	}
	var metadata uploadMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return uploadMetadata{}, fmt.Errorf("decode upload metadata: %w", err)
	}
	return metadata, nil
}

func writeUploadMetadata(tempDir string, metadata uploadMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	tempFile, err := os.CreateTemp(tempDir, ".upload-metadata-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	ok := false
	defer func() {
		_ = tempFile.Close()
		if !ok {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := tempFile.Write(data); err != nil {
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, filepath.Join(tempDir, uploadMetadataFile)); err != nil {
		return err
	}
	ok = true
	return nil
}

func sameUploadMetadata(a, b uploadMetadata) bool {
	return a.FileID == b.FileID && a.TotalChunks == b.TotalChunks && a.FileName == b.FileName && a.FileSize == b.FileSize
}

func (s *Server) reserveStorage(path string, required int64) (func(), bool, error) {
	if required < 0 {
		return nil, false, errors.New("negative storage reservation")
	}

	s.storageMu.Lock()
	available, err := diskAvailableBytes(path)
	if err != nil {
		s.storageMu.Unlock()
		return nil, false, err
	}
	requiredWithReserve := required + s.reservedStorage + minFreeDiskReserve
	if requiredWithReserve < required || available < uint64(requiredWithReserve) {
		s.storageMu.Unlock()
		return nil, false, nil
	}
	s.reservedStorage += required
	s.storageMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.storageMu.Lock()
			s.reservedStorage -= required
			s.storageMu.Unlock()
		})
	}, true, nil
}

func countActiveUploads(uploadDir, excludedFileID string) (int, error) {
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".tmp_") {
			continue
		}
		fileID := strings.TrimPrefix(entry.Name(), ".tmp_")
		if fileID != excludedFileID && validFileID(fileID) {
			count++
		}
	}
	return count, nil
}

func (s *Server) indexedFiles(uploadDir string) ([]FileItem, error) {
	s.fileIndexMu.Lock()
	defer s.fileIndexMu.Unlock()
	if s.fileIndexes == nil {
		s.fileIndexes = make(map[string]fileIndexCache)
	}

	directoryInfo, err := os.Stat(uploadDir)
	if err != nil {
		return nil, err
	}
	if cached, exists := s.fileIndexes[uploadDir]; exists && cached.directoryModTime.Equal(directoryInfo.ModTime()) {
		return append([]FileItem(nil), cached.files...), nil
	}

	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		return nil, err
	}
	files := make([]FileItem, 0, len(entries))
	for _, entry := range entries {
		name, manageable := cleanClientFilename(entry.Name())
		if entry.IsDir() || !manageable {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, FileItem{
			Name:    name,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Type:    mime.TypeByExtension(filepath.Ext(name)),
			URL:     "/api/download/" + url.PathEscape(name),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].ModTime.Equal(files[j].ModTime) {
			return files[i].Name < files[j].Name
		}
		return files[i].ModTime.After(files[j].ModTime)
	})
	s.fileIndexes[uploadDir] = fileIndexCache{
		directoryModTime: directoryInfo.ModTime(),
		files:            append([]FileItem(nil), files...),
	}
	return files, nil
}

func (s *Server) invalidateFileIndex(uploadDir string) {
	s.fileIndexMu.Lock()
	delete(s.fileIndexes, uploadDir)
	s.fileIndexMu.Unlock()
}

func filesHaveSameContent(firstPath, secondPath string) (bool, error) {
	hashFile := func(name string) ([sha256.Size]byte, error) {
		var sum [sha256.Size]byte
		file, err := os.Open(name)
		if err != nil {
			return sum, err
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return sum, err
		}
		copy(sum[:], hash.Sum(nil))
		return sum, nil
	}

	first, err := hashFile(firstPath)
	if err != nil {
		return false, err
	}
	second, err := hashFile(secondPath)
	if err != nil {
		return false, err
	}
	return first == second, nil
}

func writeUploadJSON(w http.ResponseWriter, payload map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	select {
	case s.chunkRequestSlots <- struct{}{}:
		defer func() { <-s.chunkRequestSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Too many concurrent chunk requests", http.StatusTooManyRequests)
		return
	}
	uploadDir := s.cfg.UploadDir()

	// ParseMultipartForm's argument is only a memory threshold. MaxBytesReader
	// is the actual wire-level cap for one chunk plus multipart headers/fields.
	r.Body = http.MaxBytesReader(w, r.Body, maxChunkRequestSize)
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "Upload chunk exceeds the 5 MiB limit", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Invalid multipart form", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	fileID := r.FormValue("file_id")
	chunkIndexStr := r.FormValue("chunk_index")
	totalChunksStr := r.FormValue("total_chunks")
	fileName := r.FormValue("filename")
	fileSizeStr := r.FormValue("file_size")

	if fileID == "" || chunkIndexStr == "" || totalChunksStr == "" || fileName == "" || fileSizeStr == "" {
		http.Error(w, "Missing required upload parameters", http.StatusBadRequest)
		return
	}

	chunkIndex, err := strconv.Atoi(chunkIndexStr)
	if err != nil {
		http.Error(w, "Invalid chunk_index", http.StatusBadRequest)
		return
	}
	totalChunks, err := strconv.Atoi(totalChunksStr)
	if err != nil {
		http.Error(w, "Invalid total_chunks", http.StatusBadRequest)
		return
	}
	fileSize, err := strconv.ParseInt(fileSizeStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid file_size", http.StatusBadRequest)
		return
	}
	metadata, err := validateUploadMetadata(fileID, fileName, totalChunks, fileSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if chunkIndex < 0 || chunkIndex >= metadata.TotalChunks {
		http.Error(w, "chunk_index must be smaller than total_chunks", http.StatusBadRequest)
		return
	}

	fileHeaders := r.MultipartForm.File["chunk"]
	if len(fileHeaders) != 1 {
		http.Error(w, "Exactly one chunk file is required", http.StatusBadRequest)
		return
	}
	fileHeader := fileHeaders[0]
	if fileHeader.Size < 0 || fileHeader.Size > maxChunkSize {
		http.Error(w, "Upload chunk exceeds the 5 MiB limit", http.StatusRequestEntityTooLarge)
		return
	}
	if fileHeader.Size > metadata.FileSize {
		http.Error(w, "Chunk is larger than declared file_size", http.StatusBadRequest)
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		http.Error(w, "Failed to read chunk file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	unlock := s.lockUpload(fileID)
	defer unlock()
	if s.uploadIsCancelled(fileID) {
		http.Error(w, "Upload has been cancelled", http.StatusConflict)
		return
	}
	uploadDir = s.bindUploadDirectory(fileID, uploadDir)
	tempDir := filepath.Join(uploadDir, ".tmp_"+fileID)

	storedMetadata, err := readUploadMetadata(tempDir)
	newState := os.IsNotExist(err)
	if err != nil && !newState {
		http.Error(w, "Invalid upload metadata", http.StatusConflict)
		return
	}
	if !newState && !sameUploadMetadata(storedMetadata, metadata) {
		http.Error(w, "Upload metadata conflicts with existing file_id", http.StatusConflict)
		return
	}
	stateCreated := false
	stateCommitted := !newState
	defer func() {
		if stateCommitted {
			return
		}
		if stateCreated {
			_ = os.RemoveAll(tempDir)
		}
		s.forgetUploadDirectory(fileID, uploadDir)
	}()

	reservationSize := fileHeader.Size
	if newState {
		metadataBytes, marshalErr := json.Marshal(metadata)
		if marshalErr != nil {
			http.Error(w, "Failed to encode upload metadata", http.StatusInternalServerError)
			return
		}
		reservationSize += int64(len(metadataBytes)) + 4096
	}
	releaseStorage, hasSpace, err := s.reserveStorage(uploadDir, reservationSize)
	if err != nil {
		http.Error(w, "Unable to determine remaining disk space", http.StatusInternalServerError)
		return
	}
	if !hasSpace {
		http.Error(w, "Insufficient disk space for upload chunk", http.StatusInsufficientStorage)
		return
	}
	defer releaseStorage()

	if newState {
		s.uploadStateMu.Lock()
		activeUploads, countErr := countActiveUploads(uploadDir, fileID)
		if countErr != nil {
			s.uploadStateMu.Unlock()
			http.Error(w, "Failed to inspect active uploads", http.StatusInternalServerError)
			return
		}
		if activeUploads >= maxActiveUploads {
			s.uploadStateMu.Unlock()
			http.Error(w, "Too many active uploads", http.StatusTooManyRequests)
			return
		}
		// Directories from pre-metadata versions cannot be resumed safely.
		if err := os.RemoveAll(tempDir); err != nil {
			s.uploadStateMu.Unlock()
			http.Error(w, "Failed to reset legacy upload state", http.StatusInternalServerError)
			return
		}
		if err := os.MkdirAll(tempDir, 0700); err != nil {
			s.uploadStateMu.Unlock()
			http.Error(w, "Failed to create temp chunk directory", http.StatusInternalServerError)
			return
		}
		stateCreated = true
		if err := writeUploadMetadata(tempDir, metadata); err != nil {
			s.uploadStateMu.Unlock()
			http.Error(w, "Failed to persist upload metadata", http.StatusInternalServerError)
			return
		}
		s.uploadStateMu.Unlock()
	}

	chunkPath := filepath.Join(tempDir, fmt.Sprintf("chunk_%05d", chunkIndex))
	dest, err := os.CreateTemp(tempDir, ".chunk-upload-*")
	if err != nil {
		http.Error(w, "Failed to create chunk file", http.StatusInternalServerError)
		return
	}
	tempChunkPath := dest.Name()
	keepTempChunk := false
	defer func() {
		_ = dest.Close()
		if !keepTempChunk {
			_ = os.Remove(tempChunkPath)
		}
	}()

	written, copyErr := io.Copy(dest, io.LimitReader(file, maxChunkSize+1))
	closeErr := dest.Close()
	if copyErr != nil || closeErr != nil {
		http.Error(w, "Failed to save chunk data", http.StatusInternalServerError)
		return
	}
	if written > maxChunkSize {
		http.Error(w, "Upload chunk exceeds the 5 MiB limit", http.StatusRequestEntityTooLarge)
		return
	}
	if written != fileHeader.Size {
		http.Error(w, "Chunk size changed while being read", http.StatusBadRequest)
		return
	}

	if existing, err := os.Stat(chunkPath); err == nil {
		if !existing.Mode().IsRegular() || existing.Size() != written {
			http.Error(w, "Chunk index already contains different data", http.StatusConflict)
			return
		}
		same, err := filesHaveSameContent(chunkPath, tempChunkPath)
		if err != nil {
			http.Error(w, "Failed to verify duplicate chunk", http.StatusInternalServerError)
			return
		}
		if !same {
			http.Error(w, "Chunk index already contains different data", http.StatusConflict)
			return
		}
		writeUploadJSON(w, map[string]interface{}{
			"status":      "ok",
			"chunk_index": chunkIndex,
			"filename":    metadata.FileName,
			"duplicate":   true,
		})
		return
	} else if !os.IsNotExist(err) {
		http.Error(w, "Failed to inspect existing chunk", http.StatusInternalServerError)
		return
	}

	if err := os.Rename(tempChunkPath, chunkPath); err != nil {
		http.Error(w, "Failed to publish chunk", http.StatusInternalServerError)
		return
	}
	keepTempChunk = true
	stateCommitted = true

	writeUploadJSON(w, map[string]interface{}{
		"status":      "ok",
		"chunk_index": chunkIndex,
		"filename":    metadata.FileName,
	})
}

// handleUploadStatus reports which chunks already exist for a file_id, letting
// clients resume an interrupted upload instead of restarting from zero.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uploadDir := s.cfg.UploadDir()

	fileID := r.URL.Query().Get("file_id")
	if !validFileID(fileID) {
		http.Error(w, "Invalid file_id", http.StatusBadRequest)
		return
	}

	unlock := s.lockUpload(fileID)
	defer unlock()
	if s.uploadIsCancelled(fileID) {
		http.Error(w, "Upload has been cancelled", http.StatusConflict)
		return
	}
	uploadDir = s.uploadDirectoryFor(fileID, uploadDir)

	chunks := []int{}
	tempDir := filepath.Join(uploadDir, ".tmp_"+fileID)
	metadata, metadataErr := readUploadMetadata(tempDir)
	if metadataErr == nil {
		entries, err := os.ReadDir(tempDir)
		if err != nil {
			http.Error(w, "Failed to read upload state", http.StatusInternalServerError)
			return
		}
		for _, entry := range entries {
			matches := chunkFilePattern.FindStringSubmatch(entry.Name())
			if len(matches) != 2 || entry.IsDir() {
				continue
			}
			if idx, err := strconv.Atoi(matches[1]); err == nil && idx < metadata.TotalChunks {
				chunks = append(chunks, idx)
			}
		}
		sort.Ints(chunks)
	} else if !os.IsNotExist(metadataErr) {
		http.Error(w, "Invalid upload metadata", http.StatusConflict)
		return
	}

	payload := map[string]interface{}{
		"status": "ok",
		"chunks": chunks,
	}
	if metadataErr == nil {
		payload["filename"] = metadata.FileName
		payload["total_chunks"] = metadata.TotalChunks
		payload["file_size"] = metadata.FileSize
	}
	writeUploadJSON(w, payload)
}

func (s *Server) handleCancelUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uploadDir := s.cfg.UploadDir()

	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var request struct {
		FileID string `json:"file_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !validFileID(request.FileID) {
		http.Error(w, "Invalid file_id", http.StatusBadRequest)
		return
	}

	// Mark first so a running merge can observe cancellation and queued chunks
	// cannot recreate the upload after this endpoint returns.
	s.markUploadCancelled(request.FileID)
	unlock := s.lockUpload(request.FileID)
	defer unlock()
	uploadDir = s.uploadDirectoryFor(request.FileID, uploadDir)
	if err := os.RemoveAll(filepath.Join(uploadDir, ".tmp_"+request.FileID)); err != nil {
		http.Error(w, "Failed to cancel upload", http.StatusInternalServerError)
		return
	}
	s.forgetUploadDirectory(request.FileID, uploadDir)
	writeUploadJSON(w, map[string]interface{}{"status": "ok"})
}

func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uploadDir := s.cfg.UploadDir()

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		FileID      string `json:"file_id"`
		FileName    string `json:"filename"`
		TotalChunks int    `json:"total_chunks"`
		FileSize    *int64 `json:"file_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}
	if req.FileSize == nil {
		http.Error(w, "Missing file_size", http.StatusBadRequest)
		return
	}
	requestedMetadata, err := validateUploadMetadata(req.FileID, req.FileName, req.TotalChunks, *req.FileSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	unlock := s.lockUpload(req.FileID)
	defer unlock()
	if s.uploadIsCancelled(req.FileID) {
		http.Error(w, "Upload has been cancelled", http.StatusConflict)
		return
	}
	uploadDir = s.uploadDirectoryFor(req.FileID, uploadDir)
	tempDir := filepath.Join(uploadDir, ".tmp_"+req.FileID)

	metadata, err := readUploadMetadata(tempDir)
	if os.IsNotExist(err) {
		http.Error(w, "Upload state not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Invalid upload metadata", http.StatusConflict)
		return
	}
	if !sameUploadMetadata(metadata, requestedMetadata) {
		http.Error(w, "Completion metadata conflicts with uploaded chunks", http.StatusConflict)
		return
	}

	// Verify every chunk and the exact aggregate size before allocating a
	// second on-disk copy for the atomic merge.
	chunkPaths := make([]string, metadata.TotalChunks)
	var actualSize int64
	for i := 0; i < metadata.TotalChunks; i++ {
		chunkPaths[i] = filepath.Join(tempDir, fmt.Sprintf("chunk_%05d", i))
		info, err := os.Stat(chunkPaths[i])
		if os.IsNotExist(err) {
			http.Error(w, fmt.Sprintf("Missing chunk %d", i), http.StatusBadRequest)
			return
		}
		if err != nil || !info.Mode().IsRegular() {
			http.Error(w, fmt.Sprintf("Invalid chunk %d", i), http.StatusConflict)
			return
		}
		if info.Size() < 0 || info.Size() > maxChunkSize || actualSize > maxUploadSize-info.Size() {
			http.Error(w, fmt.Sprintf("Invalid chunk size at index %d", i), http.StatusConflict)
			return
		}
		actualSize += info.Size()
	}
	if actualSize != metadata.FileSize {
		http.Error(w, "Actual chunk total does not match declared file_size", http.StatusConflict)
		return
	}

	releaseStorage, hasSpace, err := s.reserveStorage(uploadDir, actualSize)
	if err != nil {
		http.Error(w, "Unable to determine remaining disk space", http.StatusInternalServerError)
		return
	}
	if !hasSpace {
		http.Error(w, "Insufficient disk space to finalize upload", http.StatusInsufficientStorage)
		return
	}
	defer releaseStorage()

	// Merge into a temp file first so a partial merge never leaves a corrupt
	// file under the real name, then rename atomically.
	partPath := filepath.Join(tempDir, ".merged.part")
	_ = os.Remove(partPath)
	partFile, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		http.Error(w, "Failed to create destination file", http.StatusInternalServerError)
		return
	}

	var mergedSize int64
	var mergeErr error
	for _, chunkPath := range chunkPaths {
		select {
		case <-r.Context().Done():
			mergeErr = errUploadCancelled
		default:
		}
		if mergeErr != nil || s.uploadIsCancelled(req.FileID) {
			mergeErr = errUploadCancelled
			break
		}
		chunkData, err := os.Open(chunkPath)
		if err != nil {
			mergeErr = err
			break
		}
		written, err := io.Copy(partFile, chunkData)
		if err != nil {
			_ = chunkData.Close()
			mergeErr = err
			break
		}
		mergedSize += written
		_ = chunkData.Close()
	}
	if mergeErr == nil && mergedSize != metadata.FileSize {
		mergeErr = errors.New("merged size changed during finalization")
	}
	if mergeErr == nil {
		mergeErr = partFile.Sync()
	}
	if mergeErr == nil && s.uploadIsCancelled(req.FileID) {
		mergeErr = errUploadCancelled
	}
	if closeErr := partFile.Close(); mergeErr == nil {
		mergeErr = closeErr
	}

	if mergeErr != nil {
		_ = os.Remove(partPath) // roll back the partial merge
		if errors.Is(mergeErr, errUploadCancelled) {
			http.Error(w, "Upload has been cancelled", http.StatusConflict)
			return
		}
		http.Error(w, "Failed merging chunks", http.StatusInternalServerError)
		return
	}

	// Choosing a collision-free final name and publishing are one critical
	// section across different file_ids targeting the same filename.
	targetPath := filepath.Join(uploadDir, metadata.FileName)
	finalPath, err := s.publishUploadFile(req.FileID, partPath, targetPath)
	if err != nil {
		_ = os.Remove(partPath)
		if errors.Is(err, errUploadCancelled) {
			http.Error(w, "Upload has been cancelled", http.StatusConflict)
			return
		}
		http.Error(w, "Failed to finalize file", http.StatusInternalServerError)
		return
	}
	finalFileName := filepath.Base(finalPath)

	// Remove temporary chunk folder
	_ = os.RemoveAll(tempDir)
	s.forgetUploadDirectory(req.FileID, uploadDir)
	s.invalidateFileIndex(uploadDir)

	item := FileItem{
		Name:    finalFileName,
		Size:    metadata.FileSize,
		ModTime: time.Now(),
		Type:    mime.TypeByExtension(filepath.Ext(finalFileName)),
		URL:     "/api/download/" + url.PathEscape(finalFileName),
	}

	// Broadcast new file event to all clients
	s.hub.BroadcastJSON(map[string]interface{}{
		"type": "new_file",
		"data": item,
	})

	writeUploadJSON(w, map[string]interface{}{
		"status": "success",
		"file":   item,
	})
}

// CleanupTempChunks removes leftover .tmp_* upload directories older than
// maxAge, reclaiming disk space from aborted transfers.
func (s *Server) CleanupTempChunks(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	for _, uploadDir := range s.uploadDirectorySnapshot(s.cfg.UploadDir()) {
		entries, err := os.ReadDir(uploadDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".tmp_") {
				continue
			}
			fileID := strings.TrimPrefix(entry.Name(), ".tmp_")
			if !validFileID(fileID) {
				continue
			}
			unlock := s.lockUpload(fileID)
			info, err := os.Stat(filepath.Join(uploadDir, entry.Name()))
			if err == nil && info.ModTime().Before(cutoff) {
				if os.RemoveAll(filepath.Join(uploadDir, entry.Name())) == nil {
					s.forgetUploadDirectory(fileID, uploadDir)
				}
			}
			unlock()
		}
	}
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uploadDir := s.cfg.UploadDir()

	query := r.URL.Query()
	search := strings.ToLower(strings.TrimSpace(query.Get("q")))
	typeFilter := strings.ToLower(strings.TrimSpace(query.Get("type")))
	if len(search) > 256 || len(typeFilter) > 64 {
		http.Error(w, "File query is too long", http.StatusBadRequest)
		return
	}
	if typeFilter == "" {
		typeFilter = "all"
	}
	if !validFileTypeFilter(typeFilter) {
		http.Error(w, "Invalid file type filter", http.StatusBadRequest)
		return
	}

	page := 1
	pageSize := 0 // no pagination parameters preserves the legacy full list
	paginationRequested := query.Has("page") || query.Has("page_size")
	if rawPage := query.Get("page"); rawPage != "" {
		parsed, err := strconv.Atoi(rawPage)
		if err != nil || parsed < 1 || parsed > 1_000_000 {
			http.Error(w, "Invalid page", http.StatusBadRequest)
			return
		}
		page = parsed
	}
	if rawPageSize := query.Get("page_size"); rawPageSize != "" {
		parsed, err := strconv.Atoi(rawPageSize)
		if err != nil || parsed < 1 || parsed > maxFileListPageSize {
			http.Error(w, "Invalid page_size", http.StatusBadRequest)
			return
		}
		pageSize = parsed
	} else if paginationRequested {
		pageSize = defaultFileListPageSize
	}

	indexedFiles, err := s.indexedFiles(uploadDir)
	if err != nil {
		http.Error(w, "Failed to read upload directory", http.StatusInternalServerError)
		return
	}

	files := make([]FileItem, 0, len(indexedFiles))
	for _, item := range indexedFiles {
		if search != "" && !strings.Contains(strings.ToLower(item.Name), search) {
			continue
		}
		if !matchesFileType(item, typeFilter) {
			continue
		}
		files = append(files, item)
	}

	total := len(files)
	totalPages := 1
	if pageSize == 0 {
		pageSize = total
		page = 1
	} else {
		totalPages = (total + pageSize - 1) / pageSize
		if totalPages == 0 {
			totalPages = 1
		}
		start := (page - 1) * pageSize
		if start >= total {
			files = files[:0]
		} else {
			end := start + pageSize
			if end > total {
				end = total
			}
			files = files[start:end]
		}
	}

	writeUploadJSON(w, map[string]interface{}{
		"status":      "ok",
		"files":       files,
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": totalPages,
	})
}

func validFileTypeFilter(filter string) bool {
	switch filter {
	case "all", "image", "video", "audio", "document", "archive", "other":
		return true
	default:
		return false
	}
}

func matchesFileType(item FileItem, filter string) bool {
	if filter == "all" {
		return true
	}
	mediaType := strings.ToLower(item.Type)
	switch filter {
	case "image", "video", "audio":
		return strings.HasPrefix(mediaType, filter+"/")
	case "document":
		if strings.HasPrefix(mediaType, "text/") || mediaType == "application/pdf" {
			return true
		}
		switch strings.ToLower(filepath.Ext(item.Name)) {
		case ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".odp", ".rtf", ".md", ".csv":
			return true
		}
	case "archive":
		switch strings.ToLower(filepath.Ext(item.Name)) {
		case ".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".tgz", ".tbz2":
			return true
		}
	case "other":
		return !matchesFileType(item, "image") && !matchesFileType(item, "video") && !matchesFileType(item, "audio") &&
			!matchesFileType(item, "document") && !matchesFileType(item, "archive")
	}
	return false
}

// contentDisposition builds an RFC 6266 header with an ASCII fallback plus an
// RFC 5987 encoded name so non-ASCII filenames download correctly.
func contentDisposition(disposition, name string) string {
	ascii := name
	if !isASCII(name) {
		ext := filepath.Ext(name)
		ascii = "file" + ext
	}
	ascii = strings.ReplaceAll(ascii, `"`, `'`)
	return fmt.Sprintf("%s; filename=\"%s\"; filename*=UTF-8''%s", disposition, ascii, url.PathEscape(name))
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	uploadDir := s.cfg.UploadDir()
	rawName := strings.TrimPrefix(r.URL.Path, "/api/download/")
	cleanName, ok := cleanClientFilename(rawName)
	if !ok {
		http.NotFound(w, r)
		return
	}
	fullPath := filepath.Join(uploadDir, cleanName)

	info, err := os.Lstat(fullPath)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	// ?inline=1 serves without the attachment header (media preview in-page)
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", contentDisposition(disposition, cleanName))
	http.ServeFile(w, r, fullPath)
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uploadDir := s.cfg.UploadDir()

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		FileName  string   `json:"filename"`
		FileNames []string `json:"filenames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	requested := make([]string, 0, len(req.FileNames)+1)
	if req.FileName != "" {
		requested = append(requested, req.FileName)
	}
	requested = append(requested, req.FileNames...)
	if len(requested) == 0 || len(requested) > maxDeleteFiles {
		http.Error(w, "One to 100 filenames are required", http.StatusBadRequest)
		return
	}

	names := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		cleanName, ok := cleanClientFilename(name)
		if !ok {
			http.Error(w, "Invalid filename", http.StatusBadRequest)
			return
		}
		if _, exists := seen[cleanName]; exists {
			continue
		}
		seen[cleanName] = struct{}{}
		names = append(names, cleanName)
	}

	// Validate every target before deleting any of them so a malformed batch
	// cannot cause a surprising partial operation.
	for _, name := range names {
		info, err := os.Lstat(filepath.Join(uploadDir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			http.Error(w, "Failed to inspect file", http.StatusInternalServerError)
			return
		}
		if !info.Mode().IsRegular() {
			http.Error(w, "Only regular files can be deleted", http.StatusBadRequest)
			return
		}
	}

	deleted := make([]string, 0, len(names))
	defer func() {
		if len(deleted) > 0 {
			s.invalidateFileIndex(uploadDir)
		}
	}()
	for _, name := range names {
		fullPath := filepath.Join(uploadDir, name)
		if err := os.Remove(fullPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			http.Error(w, "Failed to delete file", http.StatusInternalServerError)
			return
		}
		deleted = append(deleted, name)

		s.hub.BroadcastJSON(map[string]interface{}{
			"type":     "file_deleted",
			"filename": name,
		})
	}
	writeUploadJSON(w, map[string]interface{}{
		"status":  "ok",
		"deleted": deleted,
	})
}

func getUniqueFilePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)

	counter := 1
	for {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, counter, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		counter++
	}
}
