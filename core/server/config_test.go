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
	for i := 0; i < authMaxFailures-1; i++ {
		if allowed, locked := cfg.CheckPINAttempt(ip, "wrong"); allowed || locked {
			t.Fatalf("attempt %d must be rejected without locking: allowed=%v locked=%v", i+1, allowed, locked)
		}
	}
	if allowed, locked := cfg.CheckPINAttempt(ip, "wrong"); allowed || !locked {
		t.Fatalf("final failed attempt must establish lockout: allowed=%v locked=%v", allowed, locked)
	}
	if allowed, locked := cfg.CheckPINAttempt(ip, "1234"); allowed || !locked {
		t.Fatalf("locked IP must reject the correct PIN: allowed=%v locked=%v", allowed, locked)
	}
	if allowed, locked := cfg.CheckPINAttempt("10.0.0.8", "1234"); !allowed || locked {
		t.Fatalf("unrelated IP must remain usable: allowed=%v locked=%v", allowed, locked)
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
