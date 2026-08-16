package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

type Server struct {
	cfg *Config
	hub *WSHub
}

func NewServer(cfg *Config) *Server {
	hub := NewWSHub(cfg)
	go hub.Run()

	return &Server{
		cfg: cfg,
		hub: hub,
	}
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
	mux.HandleFunc("/api/files", s.handleListFiles)
	mux.HandleFunc("/api/files/delete", s.handleDeleteFile)
	mux.HandleFunc("/api/download/", s.handleDownload)
	mux.HandleFunc("/api/text/send", s.handleSendText)
	mux.HandleFunc("/api/text/feed", s.handleTextFeed)

	// Static Web assets
	fileServer := http.FileServer(http.FS(s.cfg.StaticFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// PIN cookie check for browser page load if URL has ?pin=
		pinParam := r.URL.Query().Get("pin")
		if pinParam != "" && pinParam == s.cfg.PIN {
			http.SetCookie(w, &http.Cookie{
				Name:     "landrop_pin",
				Value:    pinParam,
				Path:     "/",
				HttpOnly: false,
				MaxAge:   86400 * 7,
			})
		}
		fileServer.ServeHTTP(w, r)
	})

	return s.authMiddleware(mux)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets and auth verification skip PIN block
		path := r.URL.Path
		if path == "/api/auth" || path == "/api/info" || strings.HasPrefix(path, "/style.css") || strings.HasPrefix(path, "/app.js") {
			next.ServeHTTP(w, r)
			return
		}

		// Check PIN from query, header, or cookie
		pin := r.URL.Query().Get("pin")
		if pin == "" {
			pin = r.Header.Get("X-PIN")
		}
		if pin == "" {
			if cookie, err := r.Cookie("landrop_pin"); err == nil {
				pin = cookie.Value
			}
		}

		// If PIN required and invalid, for API return 401, for root HTML allow loading to show PIN prompt UI
		if s.cfg.PIN != "" && pin != s.cfg.PIN {
			if strings.HasPrefix(path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  "unauthorized",
					"message": "PIN required or invalid",
				})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
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

	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if req.PIN != s.cfg.PIN {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "error",
			"message": "Invalid PIN",
		})
		return
	}

	// Set auth cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "landrop_pin",
		Value:    req.PIN,
		Path:     "/",
		HttpOnly: false,
		MaxAge:   86400 * 7,
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
