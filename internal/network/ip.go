package network

import (
	"fmt"
	"net"
	"strings"
)

// GetLocalIPs returns all valid IPv4 LAN addresses, prioritizing standard private ranges.
func GetLocalIPs() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get network interfaces: %w", err)
	}

	var primaryIPs []string
	var secondaryIPs []string

	for _, iface := range interfaces {
		// Skip down and loopback interfaces
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		name := strings.ToLower(iface.Name)
		// Deprioritize common virtual / container / VPN interfaces
		isVirtual := strings.Contains(name, "docker") ||
			strings.Contains(name, "veth") ||
			strings.Contains(name, "br-") ||
			strings.Contains(name, "vmnet") ||
			strings.Contains(name, "vbox") ||
			strings.Contains(name, "tailscale") ||
			strings.Contains(name, "tun") ||
			strings.Contains(name, "tap") ||
			strings.Contains(name, "wg") ||
			// Windows virtualization stacks (WSL, Hyper-V, NAT, mirrored adapters)
			strings.Contains(name, "vethernet") ||
			strings.Contains(name, "wsl") ||
			strings.Contains(name, "hyper-v") ||
			strings.Contains(name, "nat") ||
			strings.Contains(name, "loopback") ||
			strings.Contains(name, "virtual") ||
			// macOS / Linux extras
			strings.Contains(name, "bridge") ||
			strings.Contains(name, "awdl") ||
			strings.Contains(name, "llw") ||
			strings.Contains(name, "anbox") ||
			strings.Contains(name, "bluetooth") ||
			strings.Contains(name, "zt") ||
			strings.Contains(name, "hamachi") ||
			strings.Contains(name, "ifb")

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}

			ipStr := ip.To4().String()

			if isPrivateIP(ip) {
				if !isVirtual {
					primaryIPs = append(primaryIPs, ipStr)
				} else {
					secondaryIPs = append(secondaryIPs, ipStr)
				}
			}
		}
	}

	result := append(primaryIPs, secondaryIPs...)
	if len(result) == 0 {
		return []string{"127.0.0.1"}, nil
	}

	return result, nil
}

// FindAvailablePort starts searching from defaultPort until an open port is found.
func FindAvailablePort(startPort int) int {
	for port := startPort; port < startPort+100; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err == nil {
			_ = listener.Close()
			return port
		}
	}
	return startPort
}

func isPrivateIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12 (172.16.0.0 - 172.31.255.255)
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
	}
	return false
}
