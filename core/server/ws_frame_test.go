package server

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

func TestWSFrameRoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		[]byte("hi"),
		bytes.Repeat([]byte("x"), 125),
		bytes.Repeat([]byte("y"), 126),   // 16-bit length kicks in
		bytes.Repeat([]byte("z"), 65535), // max 16-bit
		bytes.Repeat([]byte("w"), 65536), // 64-bit length kicks in
		bytes.Repeat([]byte("q"), maxWSMessageSize), // exactly the cap
	}
	for _, payload := range cases {
		var buf bytes.Buffer
		if err := writeWSFrame(&buf, 0x1, payload); err != nil {
			t.Fatalf("writeWSFrame(len=%d) failed: %v", len(payload), err)
		}
		opcode, got, err := readWSFrame(&buf, maxWSMessageSize)
		if err != nil {
			t.Fatalf("readWSFrame(len=%d) failed: %v", len(payload), err)
		}
		if opcode != 0x1 {
			t.Fatalf("opcode mismatch: %d", opcode)
		}
		if !bytes.Equal(payload, got) {
			t.Fatalf("payload mismatch: sent %d bytes, got %d", len(payload), len(got))
		}
	}
}

func TestWSFrameRejectsOversized(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(0x81) // FIN + text
	buf.WriteByte(0x7F) // 64-bit length follows
	lenBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(lenBytes, uint64(maxWSMessageSize)+1)
	buf.Write(lenBytes)

	_, _, err := readWSFrame(&buf, maxWSMessageSize)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected oversized rejection, got err=%v", err)
	}
}

func TestWSFrameRejectsIntOverflow(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(0x81)
	buf.WriteByte(0x7F)
	lenBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(lenBytes, math.MaxUint64)
	buf.Write(lenBytes)

	_, _, err := readWSFrame(&buf, maxWSMessageSize)
	if err == nil {
		t.Fatal("expected overflow rejection, got nil error")
	}
}
