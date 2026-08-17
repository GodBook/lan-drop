package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"landrop/core/qrcode"
)

const sessionCookieName = "landrop_session"

type Server struct {
	cfg               *Config
	hub               *WSHub
	etags             map[string]string // static path -> strong ETag
	uploadMu          sync.Mutex
	uploadLocks       map[string]*uploadLock
	uploadDirectories map[string]string
	cancelledUploads  map[string]time.Time
	chunkRequestSlots chan struct{}
	uploadStateMu     sync.Mutex
	storageMu         sync.Mutex
	reservedStorage   int64
	finalizeMu        sync.Mutex
	fileIndexMu       sync.Mutex
	fileIndexes       map[string]fileIndexCache
}

func NewServer(cfg *Config) *Server {
	hub := NewWSHub(cfg)
	go hub.Run()

	return &Server{
		cfg:               cfg,
		hub:               hub,
		etags:             buildStaticETags(cfg.StaticFS),
		uploadLocks:       make(map[string]*uploadLock),
		uploadDirectories: make(map[string]string),
		cancelledUploads:  make(map[string]time.Time),
		chunkRequestSlots: make(chan struct{}, maxConcurrentChunkRequests),
		fileIndexes:       make(map[string]fileIndexCache),
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
	mux.HandleFunc("/api/qr", s.handleQR)
	mux.HandleFunc("/api/auth", s.handleAuth)
	mux.HandleFunc("/api/upload/chunk", s.handleUploadChunk)
	mux.HandleFunc("/api/upload/complete", s.handleCompleteUpload)
	mux.HandleFunc("/api/upload/status", s.handleUploadStatus)
	mux.HandleFunc("/api/upload/cancel", s.handleCancelUpload)
	mux.HandleFunc("/api/files", s.handleListFiles)
	mux.HandleFunc("/api/files/delete", s.handleDeleteFile)
	mux.HandleFunc("/api/download/", s.handleDownload)
	mux.HandleFunc("/api/text/send", s.handleSendText)
	mux.HandleFunc("/api/text/feed", s.handleTextFeed)

	// Static Web assets
	fileServer := http.FileServer(http.FS(s.cfg.StaticFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// QR scans carry the PIN once. Mint a session for a valid PIN, then
		// remove the credential from browser history for both valid and invalid
		// values. An invalid PIN lands on the normal login page after redirect.
		query := r.URL.Query()
		if query.Has("pin") {
			if allowed, _ := s.checkPINAttempt(r, query.Get("pin")); allowed {
				http.SetCookie(w, &http.Cookie{
					Name:     sessionCookieName,
					Value:    s.cfg.CreateSession(),
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
					MaxAge:   int(sessionTTL.Seconds()),
				})
			}

			cleanURL := *r.URL
			query.Del("pin")
			cleanURL.RawQuery = query.Encode()
			cleanURL.ForceQuery = false
			http.Redirect(w, r, cleanURL.RequestURI(), http.StatusSeeOther)
			return
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

	return referrerPolicyMiddleware(s.authMiddleware(mux))
}

func referrerPolicyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets and auth verification skip PIN block
		p := r.URL.Path
		if p == "/api/auth" || p == "/api/info" || p == "/style.css" || p == "/app.js" || p == "/" {
			next.ServeHTTP(w, r)
			return
		}
		if s.cfg.PIN == "" {
			next.ServeHTTP(w, r)
			return
		}

		// 1) Session cookie (set after PIN auth or QR-scan ?pin=)
		if cookie, err := r.Cookie(sessionCookieName); err == nil && s.cfg.ValidSession(cookie.Value) {
			next.ServeHTTP(w, r)
			return
		}

		// 2) PIN via query string (API scripts) or X-PIN header. Direct PIN
		// attempts share the same lockout state as /api/auth.
		query := r.URL.Query()
		pin, pinProvided := query["pin"]
		candidate := ""
		if pinProvided && len(pin) > 0 {
			candidate = pin[0]
		} else if headerPIN := r.Header.Get("X-PIN"); headerPIN != "" {
			candidate = headerPIN
			pinProvided = true
		}
		if pinProvided {
			allowed, locked := s.checkPINAttempt(r, candidate)
			if allowed {
				next.ServeHTTP(w, r)
				return
			}
			if locked && strings.HasPrefix(p, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  "locked",
					"message": "Too many failed attempts, retry in 30 seconds",
				})
				return
			}
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

func (s *Server) checkPINAttempt(r *http.Request, pin string) (allowed, locked bool) {
	return s.cfg.CheckPINAttempt(clientIP(r), pin)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"hostname":   hostname,
		"host_ip":    s.cfg.HostIP(),
		"port":       s.cfg.Port,
		"upload_dir": s.cfg.UploadDir(),
	})
}

// connectURL builds the LAN URL phones should open (PIN embedded when set).
func (s *Server) connectURL() string {
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(s.cfg.HostIP(), strconv.Itoa(s.cfg.Port)),
		Path:   "/",
	}
	if s.cfg.PIN != "" {
		query := u.Query()
		query.Set("pin", s.cfg.PIN)
		u.RawQuery = query.Encode()
	}
	return u.String()
}

// handleQR serves the connect QR code so phones can scan straight off the
// desktop/browser UI. Auth-protected because the QR (and JSON mode) exposes
// the PIN. `format=json` returns the URL and PIN for on-screen display.
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	connectURL := s.connectURL()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"url": connectURL,
			"pin": s.cfg.PIN,
		})
		return
	}

	qr, err := qrcode.Encode(connectURL, qrcode.Medium)
	if err != nil {
		http.Error(w, "failed to encode QR: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write([]byte(qr.ToSVG()))
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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

	allowed, locked := s.checkPINAttempt(r, req.PIN)
	if !allowed {
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusUnauthorized
		responseStatus := "error"
		message := "Invalid PIN"
		if locked {
			status = http.StatusTooManyRequests
			responseStatus = "locked"
			message = "Too many failed attempts, retry in 30 seconds"
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  responseStatus,
			"message": message,
		})
		return
	}

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
