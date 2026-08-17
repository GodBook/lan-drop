package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	Port     int
	PIN      string
	StaticFS fs.FS

	runtimeMu sync.RWMutex
	hostIP    string
	uploadDir string

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
		PIN:        pin,
		StaticFS:   staticFS,
		hostIP:     hostIP,
		uploadDir:  uploadDir,
		TextFeed:   make([]TextMessage, 0),
		MaxFeedLen: 50,
		feedPath:   filepath.Join(uploadDir, feedStoreFileName),
		sessions:   make(map[string]time.Time),
		authFails:  make(map[string]*authFailState),
	}
	cfg.loadFeedFromDisk()
	return cfg
}

// HostIP returns the currently advertised LAN address.
func (c *Config) HostIP() string {
	c.runtimeMu.RLock()
	defer c.runtimeMu.RUnlock()
	return c.hostIP
}

// SetHostIP changes the address used by connection links and discovery.
func (c *Config) SetHostIP(ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("invalid IPv4 address %q", ip)
	}
	c.runtimeMu.Lock()
	c.hostIP = parsed.To4().String()
	c.runtimeMu.Unlock()
	return nil
}

// UploadDir returns the directory currently used for received files.
func (c *Config) UploadDir() string {
	c.runtimeMu.RLock()
	defer c.runtimeMu.RUnlock()
	return c.uploadDir
}

// SetUploadDir switches future file operations to dir and loads that
// directory's text feed. Existing in-flight requests keep their local path.
func (c *Config) SetUploadDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("upload directory cannot be empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve upload directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return fmt.Errorf("create upload directory: %w", err)
	}

	c.runtimeMu.Lock()
	c.uploadDir = abs
	c.runtimeMu.Unlock()

	c.mu.Lock()
	c.feedPath = filepath.Join(abs, feedStoreFileName)
	c.TextFeed = make([]TextMessage, 0)
	c.loadFeedFromDiskLocked()
	c.mu.Unlock()
	return nil
}

// CheckPIN compares a candidate PIN in constant time. An empty configured PIN
// means authentication is disabled and everything passes.
func (c *Config) CheckPIN(input string) bool {
	if c.PIN == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(input), []byte(c.PIN)) == 1
}

// CheckPINAttempt atomically applies the per-IP lockout policy and verifies a
// candidate PIN. Keeping the whole decision under authMu prevents concurrent
// requests from passing the lock check before their failures are recorded.
func (c *Config) CheckPINAttempt(ip, input string) (allowed, locked bool) {
	if c.PIN == "" {
		return true, false
	}

	c.authMu.Lock()
	defer c.authMu.Unlock()

	now := time.Now()
	state, exists := c.authFails[ip]
	if exists && now.Before(state.lockedUntil) {
		return false, true
	}
	if c.CheckPIN(input) {
		delete(c.authFails, ip)
		return true, false
	}
	if !exists {
		state = &authFailState{}
		c.authFails[ip] = state
	}
	state.lockedUntil = time.Time{}
	state.count++
	if state.count >= authMaxFailures {
		state.count = 0
		state.lockedUntil = now.Add(authLockDuration)
		return false, true
	}
	return false, false
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadFeedFromDiskLocked()
}

func (c *Config) loadFeedFromDiskLocked() {
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
