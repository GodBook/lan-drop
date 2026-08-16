package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TextMessage represents a shared clipboard text item
type TextMessage struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Sender    string    `json:"sender"`
	Timestamp time.Time `json:"timestamp"`
}

// FileItem represents an uploaded file
type FileItem struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Type    string    `json:"type"`
	URL     string    `json:"url"`
}

const (
	sessionTTL        = 7 * 24 * time.Hour
	authMaxFailures   = 5
	authLockDuration  = 30 * time.Second
	feedStoreFileName = ".landrop_feed.json"
)

type authFailState struct {
	count       int
	lockedUntil time.Time
}

// Config holds runtime configuration and in-memory states
type Config struct {
	Port      int
	HostIP    string
	UploadDir string
	PIN       string
	StaticFS  fs.FS

	mu         sync.RWMutex
	TextFeed   []TextMessage
	MaxFeedLen int
	feedPath   string

	sessions  map[string]time.Time // random session token -> expiry
	authMu    sync.Mutex
	authFails map[string]*authFailState
}

// NewConfig initializes default configuration
func NewConfig(port int, hostIP, uploadDir, pin string, staticFS fs.FS) *Config {
	if uploadDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			uploadDir = filepath.Join(home, "Downloads", "LAN_Drop")
		} else {
			uploadDir = "./LAN_Drop_Files"
		}
	}
	_ = os.MkdirAll(uploadDir, 0755)

	// An empty PIN explicitly means "auth disabled"; callers decide whether to
	// generate a random one (see GenerateRandomPIN).
	cfg := &Config{
		Port:       port,
		HostIP:     hostIP,
		UploadDir:  uploadDir,
		PIN:        pin,
		StaticFS:   staticFS,
		TextFeed:   make([]TextMessage, 0),
		MaxFeedLen: 50,
		feedPath:   filepath.Join(uploadDir, feedStoreFileName),
		sessions:   make(map[string]time.Time),
		authFails:  make(map[string]*authFailState),
	}
	cfg.loadFeedFromDisk()
	return cfg
}

// CheckPIN compares a candidate PIN in constant time. An empty configured PIN
// means authentication is disabled and everything passes.
func (c *Config) CheckPIN(input string) bool {
	if c.PIN == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(input), []byte(c.PIN)) == 1
}

// CreateSession mints a random 256-bit session token valid for sessionTTL.
// Sessions decouple the browser cookie from the PIN itself.
func (c *Config) CreateSession() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is practically fatal; fall back to time-based token
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	token := hex.EncodeToString(buf)
	c.mu.Lock()
	c.sessions[token] = time.Now().Add(sessionTTL)
	// Opportunistic sweep of expired entries
	now := time.Now()
	for t, exp := range c.sessions {
		if now.After(exp) {
			delete(c.sessions, t)
		}
	}
	c.mu.Unlock()
	return token
}

// ValidSession reports whether the token belongs to a live session.
func (c *Config) ValidSession(token string) bool {
	if token == "" {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	exp, ok := c.sessions[token]
	return ok && time.Now().Before(exp)
}

// IsAuthLocked reports whether the source IP is temporarily locked out after
// repeated PIN failures.
func (c *Config) IsAuthLocked(ip string) bool {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	st, ok := c.authFails[ip]
	return ok && time.Now().Before(st.lockedUntil)
}

// RecordAuthFailure counts a failed attempt and locks the IP after
// authMaxFailures consecutive failures.
func (c *Config) RecordAuthFailure(ip string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	st, ok := c.authFails[ip]
	if !ok {
		st = &authFailState{}
		c.authFails[ip] = st
	}
	st.count++
	if st.count >= authMaxFailures {
		st.lockedUntil = time.Now().Add(authLockDuration)
		st.count = 0
	}
}

// ClearAuthFailures resets the failure counter after a successful login.
func (c *Config) ClearAuthFailures(ip string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	delete(c.authFails, ip)
}

func (c *Config) AddTextMessage(msg TextMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.TextFeed = append([]TextMessage{msg}, c.TextFeed...)
	if len(c.TextFeed) > c.MaxFeedLen {
		c.TextFeed = c.TextFeed[:c.MaxFeedLen]
	}
	c.saveFeedLocked()
}

func (c *Config) GetTextFeed() []TextMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	res := make([]TextMessage, len(c.TextFeed))
	copy(res, c.TextFeed)
	return res
}

// loadFeedFromDisk restores the text history persisted by previous runs.
func (c *Config) loadFeedFromDisk() {
	data, err := os.ReadFile(c.feedPath)
	if err != nil {
		return
	}
	var feed []TextMessage
	if json.Unmarshal(data, &feed) == nil && len(feed) > 0 {
		if len(feed) > c.MaxFeedLen {
			feed = feed[:c.MaxFeedLen]
		}
		c.TextFeed = feed
	}
}

// saveFeedLocked persists the feed atomically (temp file + rename); caller must
// hold c.mu.
func (c *Config) saveFeedLocked() {
	data, err := json.Marshal(c.TextFeed)
	if err != nil {
		return
	}
	tmp := c.feedPath + ".tmp"
	if os.WriteFile(tmp, data, 0600) == nil {
		_ = os.Rename(tmp, c.feedPath)
	}
}

func GenerateRandomPIN(length int) string {
	digits := "0123456789"
	pin := make([]byte, length)
	for i := 0; i < length; i++ {
		num, _ := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		pin[i] = digits[num.Int64()]
	}
	return string(pin)
}

func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
