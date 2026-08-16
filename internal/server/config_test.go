package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestConfig(t *testing.T, pin string) *Config {
	t.Helper()
	return NewConfig(8087, "127.0.0.1", t.TempDir(), pin, nil)
}

func TestCheckPIN(t *testing.T) {
	cfg := newTestConfig(t, "1234")
	if !cfg.CheckPIN("1234") {
		t.Fatal("correct PIN rejected")
	}
	if cfg.CheckPIN("0000") {
		t.Fatal("wrong PIN accepted")
	}
	if cfg.CheckPIN("") {
		t.Fatal("empty PIN accepted")
	}

	// Disabled auth (empty PIN) accepts everything
	open := newTestConfig(t, "")
	if !open.CheckPIN("") || !open.CheckPIN("anything") {
		t.Fatal("disabled PIN must accept all")
	}
}

func TestSessionLifecycle(t *testing.T) {
	cfg := newTestConfig(t, "1234")
	token := cfg.CreateSession()
	if token == "" {
		t.Fatal("empty session token")
	}
	if !cfg.ValidSession(token) {
		t.Fatal("fresh session invalid")
	}
	if cfg.ValidSession("bogus-token") {
		t.Fatal("bogus token accepted")
	}

	// Expired sessions must not validate
	cfg.mu.Lock()
	cfg.sessions[token] = time.Now().Add(-time.Second)
	cfg.mu.Unlock()
	if cfg.ValidSession(token) {
		t.Fatal("expired session accepted")
	}
}

func TestAuthRateLimit(t *testing.T) {
	cfg := newTestConfig(t, "1234")
	ip := "10.0.0.9"
	for i := 0; i < authMaxFailures; i++ {
		cfg.RecordAuthFailure(ip)
	}
	if !cfg.IsAuthLocked(ip) {
		t.Fatal("IP should be locked after repeated failures")
	}
	if cfg.IsAuthLocked("10.0.0.8") {
		t.Fatal("unrelated IP must not be locked")
	}
	cfg.ClearAuthFailures(ip)
	if cfg.IsAuthLocked(ip) {
		t.Fatal("lock must clear on success")
	}
}

func TestTextFeedRingAndPersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := NewConfig(8087, "127.0.0.1", dir, "1234", nil)

	for i := 0; i < cfg.MaxFeedLen+10; i++ {
		cfg.AddTextMessage(TextMessage{ID: string(rune('a' + i%26)), Content: "m", Sender: "t", Timestamp: time.Now()})
	}
	feed := cfg.GetTextFeed()
	if len(feed) != cfg.MaxFeedLen {
		t.Fatalf("ring buffer must cap at %d, got %d", cfg.MaxFeedLen, len(feed))
	}

	// Feed must be persisted and reloaded by a fresh config on the same dir
	if _, err := os.Stat(filepath.Join(dir, feedStoreFileName)); err != nil {
		t.Fatalf("feed store missing: %v", err)
	}
	reloaded := NewConfig(8087, "127.0.0.1", dir, "1234", nil)
	if len(reloaded.GetTextFeed()) != cfg.MaxFeedLen {
		t.Fatalf("feed not restored: got %d items", len(reloaded.GetTextFeed()))
	}
}
