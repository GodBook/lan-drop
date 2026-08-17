package discovery

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

func TestBuildResponseDoesNotExposePIN(t *testing.T) {
	packet := buildResponse(0, mdnsTTL, "LAN Drop - pc."+ServiceType, "pc.local.", 8087, net.ParseIP("192.168.1.8"), []string{"path=/", "version=1", "pin=required"})
	if len(packet) < 12 || binary.BigEndian.Uint16(packet[6:8]) != 4 {
		t.Fatalf("unexpected response header: %x", packet)
	}
	if strings.Contains(string(packet), "1234") || !strings.Contains(string(packet), "pin=required") {
		t.Fatal("announcement must expose only the PIN requirement")
	}
}

func TestQueryRequestsService(t *testing.T) {
	query := make([]byte, 12)
	binary.BigEndian.PutUint16(query[0:2], 42)
	binary.BigEndian.PutUint16(query[4:6], 1)
	query = append(query, encodeName(ServiceType)...)
	query = append(query, 0, dnsTypePTR, 0, 1)
	id, ok := queryRequestsService(query, "LAN Drop - pc."+ServiceType, "pc.local.")
	if !ok || id != 42 {
		t.Fatalf("service query not recognized: id=%d ok=%v", id, ok)
	}
}

func TestSafeDNSLabel(t *testing.T) {
	if got := safeDNSLabel(" 办公 PC_01.local "); got != "PC-01-local" {
		t.Fatalf("safeDNSLabel returned %q", got)
	}
	if got := safeDNSLabel("中文"); got != "landrop" {
		t.Fatalf("non-ASCII fallback returned %q", got)
	}
}

func TestInterfaceForIPv4FindsLoopback(t *testing.T) {
	if _, err := interfaceForIPv4(net.ParseIP("127.0.0.1")); err != nil {
		t.Fatalf("loopback interface lookup failed: %v", err)
	}
}

func TestInterfaceForIPv4RejectsUnknownAddress(t *testing.T) {
	if _, err := interfaceForIPv4(net.ParseIP("203.0.113.254")); err == nil {
		t.Fatal("unknown IPv4 address must not resolve to an interface")
	}
}
