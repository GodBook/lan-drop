package qrcode

import (
	"strings"
	"testing"
)

func TestEncodeBasicMatrix(t *testing.T) {
	qr, err := Encode("http://192.168.1.5:8087/?pin=1234", Medium)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	if qr.Size < 21 {
		t.Fatalf("matrix too small: %d (min QR version 1 is 21)", qr.Size)
	}
	if qr.Size%2 == 0 {
		t.Fatalf("QR size must be odd, got %d", qr.Size)
	}
	// Finder patterns: 7x7 solid dark border at three corners
	corners := [][2]int{{0, 0}, {qr.Size - 7, 0}, {0, qr.Size - 7}}
	for _, c := range corners {
		r0, c0 := c[0], c[1]
		for d := 0; d < 7; d++ {
			if !qr.Modules[r0][c0+d] {
				t.Errorf("finder border not dark at corner %v offset %d", c, d)
			}
			if !qr.Modules[r0+d][c0] {
				t.Errorf("finder border not dark at corner %v offset %d", c, d)
			}
		}
	}
}

func TestEncodeLongURL(t *testing.T) {
	long := "http://192.168.100.200:8087/?pin=9999&x=" + strings.Repeat("a", 200)
	qr, err := Encode(long, Medium)
	if err != nil {
		t.Fatalf("Encode long URL failed: %v", err)
	}
	if qr.Size > 17+4*10 {
		t.Fatalf("encoder must cap at version 10 (size %d)", qr.Size)
	}
}

func TestToSmallTerminalString(t *testing.T) {
	qr, err := Encode("hello", Medium)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	out := qr.ToSmallTerminalString()
	if !strings.Contains(out, "\n") {
		t.Fatal("terminal rendering must be multi-line")
	}
	// The border guarantees at least one blank column; rendering must be non-empty
	if len(strings.TrimSpace(out)) == 0 {
		t.Fatal("terminal rendering is empty")
	}
}
