package server

import (
	"encoding/json"
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
	"time"
)

type ChunkUploadRequest struct {
	FileID      string `json:"file_id"`
	ChunkIndex  int    `json:"chunk_index"`
	TotalChunks int    `json:"total_chunks"`
	FileName    string `json:"filename"`
	FileSize    int64  `json:"file_size"`
}

// validFileID guards the temp-directory naming against path tricks.
var fileIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validFileID(id string) bool {
	return fileIDPattern.MatchString(id)
}

func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 32MB max memory for multipart
	err := r.ParseMultipartForm(32 << 20)
	if err != nil {
		http.Error(w, "Invalid multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	fileID := r.FormValue("file_id")
	chunkIndexStr := r.FormValue("chunk_index")
	totalChunksStr := r.FormValue("total_chunks")
	fileName := r.FormValue("filename")

	if fileID == "" || chunkIndexStr == "" || totalChunksStr == "" || fileName == "" {
		http.Error(w, "Missing required upload parameters", http.StatusBadRequest)
		return
	}
	if !validFileID(fileID) {
		http.Error(w, "Invalid file_id", http.StatusBadRequest)
		return
	}

	chunkIndex, err := strconv.Atoi(chunkIndexStr)
	if err != nil || chunkIndex < 0 {
		http.Error(w, "Invalid chunk_index", http.StatusBadRequest)
		return
	}
	totalChunks, err := strconv.Atoi(totalChunksStr)
	if err != nil || totalChunks < 1 || totalChunks > 1_000_000 {
		http.Error(w, "Invalid total_chunks", http.StatusBadRequest)
		return
	}
	cleanFileName := filepath.Base(fileName)

	file, _, err := r.FormFile("chunk")
	if err != nil {
		http.Error(w, "Failed to read chunk file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Store in temp directory
	tempDir := filepath.Join(s.cfg.UploadDir, ".tmp_"+fileID)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		http.Error(w, "Failed to create temp chunk dir: "+err.Error(), http.StatusInternalServerError)
		return
	}

	chunkPath := filepath.Join(tempDir, fmt.Sprintf("chunk_%05d", chunkIndex))
	dest, err := os.Create(chunkPath)
	if err != nil {
		http.Error(w, "Failed to write chunk: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer dest.Close()

	if _, err := io.Copy(dest, file); err != nil {
		http.Error(w, "Failed to save chunk data: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"chunk_index": chunkIndex,
		"filename":    cleanFileName,
	})
}

// handleUploadStatus reports which chunks already exist for a file_id, letting
// clients resume an interrupted upload instead of restarting from zero.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	fileID := r.URL.Query().Get("file_id")
	if !validFileID(fileID) {
		http.Error(w, "Invalid file_id", http.StatusBadRequest)
		return
	}

	chunks := []int{}
	tempDir := filepath.Join(s.cfg.UploadDir, ".tmp_"+fileID)
	if entries, err := os.ReadDir(tempDir); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "chunk_") {
				continue
			}
			if idx, err := strconv.Atoi(strings.TrimPrefix(name, "chunk_")); err == nil {
				chunks = append(chunks, idx)
			}
		}
		sort.Ints(chunks)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"chunks": chunks,
	})
}

func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		FileID      string `json:"file_id"`
		FileName    string `json:"filename"`
		TotalChunks int    `json:"total_chunks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}
	if !validFileID(req.FileID) {
		http.Error(w, "Invalid file_id", http.StatusBadRequest)
		return
	}
	if req.TotalChunks < 1 {
		http.Error(w, "Invalid total_chunks", http.StatusBadRequest)
		return
	}

	cleanFileName := filepath.Base(req.FileName)
	tempDir := filepath.Join(s.cfg.UploadDir, ".tmp_"+req.FileID)

	// Pre-check every chunk exists before touching the destination
	chunkPaths := make([]string, req.TotalChunks)
	for i := 0; i < req.TotalChunks; i++ {
		chunkPaths[i] = filepath.Join(tempDir, fmt.Sprintf("chunk_%05d", i))
		if _, err := os.Stat(chunkPaths[i]); err != nil {
			http.Error(w, fmt.Sprintf("Missing chunk %d: %v", i, err), http.StatusBadRequest)
			return
		}
	}

	// Merge into a temp file first so a partial merge never leaves a corrupt
	// file under the real name, then rename atomically.
	targetPath := filepath.Join(s.cfg.UploadDir, cleanFileName)
	partPath := targetPath + ".part"
	partFile, err := os.Create(partPath)
	if err != nil {
		http.Error(w, "Failed to create destination file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	mergeFailed := false
	for _, chunkPath := range chunkPaths {
		chunkData, err := os.Open(chunkPath)
		if err != nil {
			mergeFailed = true
			_ = chunkData.Close()
			break
		}
		if _, err := io.Copy(partFile, chunkData); err != nil {
			_ = chunkData.Close()
			mergeFailed = true
			break
		}
		_ = chunkData.Close()
	}
	_ = partFile.Close()

	if mergeFailed {
		_ = os.Remove(partPath) // roll back the partial merge
		http.Error(w, "Failed merging chunks", http.StatusInternalServerError)
		return
	}

	// Ensure unique final name if collision exists, then publish
	finalPath := getUniqueFilePath(targetPath)
	if err := os.Rename(partPath, finalPath); err != nil {
		_ = os.Remove(partPath)
		http.Error(w, "Failed to finalize file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	finalFileName := filepath.Base(finalPath)

	// Remove temporary chunk folder
	_ = os.RemoveAll(tempDir)

	stat, _ := os.Stat(finalPath)
	item := FileItem{
		Name:    finalFileName,
		Size:    stat.Size(),
		ModTime: time.Now(),
		Type:    mime.TypeByExtension(filepath.Ext(finalFileName)),
		URL:     "/api/download/" + url.PathEscape(finalFileName),
	}

	// Broadcast new file event to all clients
	s.hub.BroadcastJSON(map[string]interface{}{
		"type": "new_file",
		"data": item,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "success",
		"file":   item,
	})
}

// CleanupTempChunks removes leftover .tmp_* upload directories older than
// maxAge, reclaiming disk space from aborted transfers.
func (s *Server) CleanupTempChunks(maxAge time.Duration) {
	entries, err := os.ReadDir(s.cfg.UploadDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".tmp_") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(s.cfg.UploadDir, entry.Name()))
		}
	}
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.cfg.UploadDir)
	if err != nil {
		http.Error(w, "Failed to read upload directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var files []FileItem
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, FileItem{
			Name:    entry.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Type:    mime.TypeByExtension(filepath.Ext(entry.Name())),
			URL:     "/api/download/" + url.PathEscape(entry.Name()),
		})
	}

	// Sort newest first
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"files":  files,
	})
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
	rawName := strings.TrimPrefix(r.URL.Path, "/api/download/")
	cleanName := filepath.Base(rawName)
	fullPath := filepath.Join(s.cfg.UploadDir, cleanName)

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
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
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		FileName string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	cleanName := filepath.Base(req.FileName)
	fullPath := filepath.Join(s.cfg.UploadDir, cleanName)

	_ = os.Remove(fullPath)

	s.hub.BroadcastJSON(map[string]interface{}{
		"type":     "file_deleted",
		"filename": cleanName,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
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
