package server

import (
	"crypto/rand"
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
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	Type      string    `json:"type"`
	URL       string    `json:"url"`
}

// Config holds runtime configuration and in-memory states
type Config struct {
	Port       int
	HostIP     string
	UploadDir  string
	PIN        string
	StaticFS   fs.FS
	
	mu         sync.RWMutex
	TextFeed   []TextMessage
	MaxFeedLen int
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

	if pin == "" {
		pin = generateRandomPIN(4)
	}

	return &Config{
		Port:       port,
		HostIP:     hostIP,
		UploadDir:  uploadDir,
		PIN:        pin,
		StaticFS:   staticFS,
		TextFeed:   make([]TextMessage, 0),
		MaxFeedLen: 50,
	}
}

func (c *Config) AddTextMessage(msg TextMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.TextFeed = append([]TextMessage{msg}, c.TextFeed...)
	if len(c.TextFeed) > c.MaxFeedLen {
		c.TextFeed = c.TextFeed[:c.MaxFeedLen]
	}
}

func (c *Config) GetTextFeed() []TextMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	res := make([]TextMessage, len(c.TextFeed))
	copy(res, c.TextFeed)
	return res
}

func generateRandomPIN(length int) string {
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
