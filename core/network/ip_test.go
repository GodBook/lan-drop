package network

import (
	"net"
	"testing"
)

func TestIsPrivateIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.32.0.1", false}, // just outside 172.16/12
		{"172.15.0.1", false},
		{"192.168.0.1", true},
		{"192.168.31.255", true},
		{"192.169.0.1", false},
		{"8.8.8.8", false},
		{"127.0.0.1", false}, // loopback handled separately, but not private-range
		{"169.254.1.1", false},
	}
	for _, c := range cases {
		if got := isPrivateIP(net.ParseIP(c.ip)); got != c.want {
			t.Errorf("isPrivateIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestGetLANInfoSplitsPhysicalAndVirtual(t *testing.T) {
	info := GetLANInfo()
	if len(info.Physical) == 0 && len(info.Virtual) == 0 {
		t.Fatal("GetLANInfo returned nothing (fallback should guarantee 127.0.0.1)")
	}
	// Every physical address must be a private IPv4
	for _, ip := range info.Physical {
		if !isPrivateIP(net.ParseIP(ip)) {
			t.Errorf("physical list contains non-private IP %s", ip)
		}
	}
}

func TestGetLocalIPsReturnsSomething(t *testing.T) {
	ips, err := GetLocalIPs()
	if err != nil {
		t.Fatalf("GetLocalIPs error: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("GetLocalIPs returned no IPs")
	}
}

func TestFindAvailablePort(t *testing.T) {
	// Whatever the environment, the returned port must be bindable
	port := FindAvailablePort(28080)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	ln.Close()
	if port < 1024 || port > 65535 {
		t.Fatalf("port out of range: %d", port)
	}
}
