package server

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
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

	chunkIndex, _ := strconv.Atoi(chunkIndexStr)
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

func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		FileID      string `json:"file_id"`
		FileName    string `json:"filename"`
		TotalChunks int    `json:"total_chunks"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	cleanFileName := filepath.Base(req.FileName)
	tempDir := filepath.Join(s.cfg.UploadDir, ".tmp_"+req.FileID)

	// Ensure unique file name if collision exists
	targetPath := filepath.Join(s.cfg.UploadDir, cleanFileName)
	targetPath = getUniqueFilePath(targetPath)
	finalFileName := filepath.Base(targetPath)

	finalFile, err := os.Create(targetPath)
	if err != nil {
		http.Error(w, "Failed to create destination file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer finalFile.Close()

	// Sequentially merge all chunks
	for i := 0; i < req.TotalChunks; i++ {
		chunkPath := filepath.Join(tempDir, fmt.Sprintf("chunk_%05d", i))
		chunkData, err := os.Open(chunkPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("Missing chunk %d: %v", i, err), http.StatusBadRequest)
			return
		}
		if _, err := io.Copy(finalFile, chunkData); err != nil {
			_ = chunkData.Close()
			http.Error(w, "Failed merging chunk: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = chunkData.Close()
	}

	// Remove temporary chunk folder
	_ = os.RemoveAll(tempDir)

	stat, _ := finalFile.Stat()
	item := FileItem{
		Name:    finalFileName,
		Size:    stat.Size(),
		ModTime: time.Now(),
		Type:    mime.TypeByExtension(filepath.Ext(finalFileName)),
		URL:     "/api/download/" + finalFileName,
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

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.cfg.UploadDir)
	if err != nil {
		http.Error(w, "Failed to read upload directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var files []FileItem
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
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
			URL:     "/api/download/" + entry.Name(),
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

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	filename := strings.TrimPrefix(r.URL.Path, "/api/download/")
	cleanName := filepath.Base(filename)
	fullPath := filepath.Join(s.cfg.UploadDir, cleanName)

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}

	// Range request support for resuming and speed
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", cleanName))
	http.ServeFile(w, r, fullPath)
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

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
