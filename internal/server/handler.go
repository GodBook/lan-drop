package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

const sessionCookieName = "landrop_session"

type Server struct {
	cfg   *Config
	hub   *WSHub
	etags map[string]string // static path -> strong ETag
}

func NewServer(cfg *Config) *Server {
	hub := NewWSHub(cfg)
	go hub.Run()

	return &Server{
		cfg:   cfg,
		hub:   hub,
		etags: buildStaticETags(cfg.StaticFS),
	}
}

// Shutdown stops the WebSocket hub and closes all live client connections.
func (s *Server) Shutdown() {
	s.hub.Shutdown()
}

// buildStaticETags precomputes strong ETags for the embedded assets so browsers
// can revalidate cheaply (the embedded FS has no modtime for weak validators).
func buildStaticETags(fsys fs.FS) map[string]string {
	etags := make(map[string]string)
	for _, name := range []string{"index.html", "app.js", "style.css"} {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`
		etags["/"+name] = etag
		if name == "index.html" {
			etags["/"] = etag
			etags["/index.html"] = etag
		}
	}
	return etags
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// WebSocket endpoint
	mux.HandleFunc("/api/ws", s.hub.ServeWebSocket)

	// API endpoints
	mux.HandleFunc("/api/info", s.handleInfo)
	mux.HandleFunc("/api/auth", s.handleAuth)
	mux.HandleFunc("/api/upload/chunk", s.handleUploadChunk)
	mux.HandleFunc("/api/upload/complete", s.handleCompleteUpload)
	mux.HandleFunc("/api/upload/status", s.handleUploadStatus)
	mux.HandleFunc("/api/files", s.handleListFiles)
	mux.HandleFunc("/api/files/delete", s.handleDeleteFile)
	mux.HandleFunc("/api/download/", s.handleDownload)
	mux.HandleFunc("/api/text/send", s.handleSendText)
	mux.HandleFunc("/api/text/feed", s.handleTextFeed)

	// Static Web assets
	fileServer := http.FileServer(http.FS(s.cfg.StaticFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// A valid ?pin= on the page URL (QR scan flow) mints a session cookie
		pinParam := r.URL.Query().Get("pin")
		if pinParam != "" && s.cfg.CheckPIN(pinParam) {
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookieName,
				Value:    s.cfg.CreateSession(),
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   int(sessionTTL.Seconds()),
			})
		}

		// Cheap revalidation for embedded assets
		if etag, ok := s.etags[path.Clean(r.URL.Path)]; ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", etag)
			if strings.Contains(r.Header.Get("If-None-Match"), etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})

	return s.authMiddleware(mux)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets and auth verification skip PIN block
		p := r.URL.Path
		if p == "/api/auth" || p == "/api/info" || p == "/style.css" || p == "/app.js" || p == "/" {
			next.ServeHTTP(w, r)
			return
		}

		// 1) Session cookie (set after PIN auth or QR-scan ?pin=)
		if cookie, err := r.Cookie(sessionCookieName); err == nil && s.cfg.ValidSession(cookie.Value) {
			next.ServeHTTP(w, r)
			return
		}

		// 2) PIN via query string (QR code / API scripts) or X-PIN header
		pin := r.URL.Query().Get("pin")
		if pin == "" {
			pin = r.Header.Get("X-PIN")
		}
		if s.cfg.CheckPIN(pin) {
			next.ServeHTTP(w, r)
			return
		}

		// Not authorized: JSON 401 for API paths, serve the page for "/" so the
		// PIN prompt UI can load.
		if strings.HasPrefix(p, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "unauthorized",
				"message": "PIN required or invalid",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the host part of RemoteAddr for rate limiting.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"hostname":   hostname,
		"host_ip":    s.cfg.HostIP,
		"port":       s.cfg.Port,
		"upload_dir": s.cfg.UploadDir,
	})
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ip := clientIP(r)
	if s.cfg.IsAuthLocked(ip) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "locked",
			"message": "Too many failed attempts, retry in 30 seconds",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if !s.cfg.CheckPIN(req.PIN) {
		s.cfg.RecordAuthFailure(ip)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "error",
			"message": "Invalid PIN",
		})
		return
	}
	s.cfg.ClearAuthFailures(ip)

	// Mint a random session token; the raw PIN never becomes the credential
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.cfg.CreateSession(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
	})
}

func (s *Server) handleSendText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB text cap
	var req struct {
		Content string `json:"content"`
		Sender  string `json:"sender"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Content) == "" {
		http.Error(w, "Content cannot be empty", http.StatusBadRequest)
		return
	}

	sender := req.Sender
	if sender == "" {
		sender = "Guest"
	}

	msg := TextMessage{
		ID:        time.Now().Format("20060102150405"),
		Content:   req.Content,
		Sender:    sender,
		Timestamp: time.Now(),
	}

	s.cfg.AddTextMessage(msg)
	s.hub.BroadcastJSON(map[string]interface{}{
		"type": "new_text",
		"data": msg,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"data":   msg,
	})
}

func (s *Server) handleTextFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"feed":   s.cfg.GetTextFeed(),
	})
}
