// Package discovery advertises LAN Drop through multicast DNS so native
// clients can find the service without knowing an IP address in advance.
package discovery

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	ServiceType = "_landrop._tcp.local."
	mdnsTTL     = 120

	dnsTypeA   = 1
	dnsTypePTR = 12
	dnsTypeTXT = 16
	dnsTypeSRV = 33
	dnsTypeANY = 255
)

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Advertiser owns the multicast socket and announcement lifecycle.
type Advertiser struct {
	conn       *net.UDPConn
	instance   string
	host       string
	port       int
	ip         net.IP
	txt        []string
	stop       chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
	writeMutex sync.Mutex
}

// Start advertises a LAN Drop HTTP endpoint. The PIN value is never included;
// clients only learn whether authentication will be required after connecting.
func Start(hostLabel, ip string, port int, pinRequired bool) (*Advertiser, error) {
	ipv4 := net.ParseIP(ip).To4()
	if ipv4 == nil {
		return nil, fmt.Errorf("discovery: invalid IPv4 address %q", ip)
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("discovery: invalid port %d", port)
	}

	label := safeDNSLabel(hostLabel)
	iface, err := interfaceForIPv4(ipv4)
	if err != nil {
		return nil, fmt.Errorf("discovery: find interface for %s: %w", ipv4, err)
	}
	conn, err := net.ListenMulticastUDP("udp4", iface, mdnsGroup)
	if err != nil {
		return nil, fmt.Errorf("discovery: listen mDNS: %w", err)
	}
	_ = conn.SetReadBuffer(64 << 10)

	pinMode := "none"
	if pinRequired {
		pinMode = "required"
	}
	a := &Advertiser{
		conn:     conn,
		instance: "LAN Drop - " + label + "." + ServiceType,
		host:     label + ".local.",
		port:     port,
		ip:       append(net.IP(nil), ipv4...),
		txt:      []string{"path=/", "version=1", "pin=" + pinMode},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go a.run()
	return a, nil
}

func interfaceForIPv4(ip net.IP) (*net.Interface, error) {
	wanted := ip.To4()
	if wanted == nil {
		return nil, fmt.Errorf("invalid IPv4 address %q", ip)
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range interfaces {
		iface := &interfaces[i]
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var candidate net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				candidate = value.IP
			case *net.IPAddr:
				candidate = value.IP
			}
			if candidate != nil && candidate.To4() != nil && candidate.To4().Equal(wanted) {
				return iface, nil
			}
		}
	}
	return nil, fmt.Errorf("no active interface owns %s", wanted)
}

// Close sends a zero-TTL goodbye and releases the multicast socket.
func (a *Advertiser) Close() error {
	if a == nil {
		return nil
	}
	var closeErr error
	a.closeOnce.Do(func() {
		a.sendAnnouncement(0, mdnsGroup)
		close(a.stop)
		closeErr = a.conn.Close()
		<-a.done
	})
	return closeErr
}

func (a *Advertiser) run() {
	defer close(a.done)
	a.sendAnnouncement(mdnsTTL, mdnsGroup)
	nextAnnouncement := time.Now().Add(60 * time.Second)
	buf := make([]byte, 1500)

	for {
		_ = a.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, source, err := a.conn.ReadFromUDP(buf)
		if err == nil && n >= 12 {
			id, shouldReply := queryRequestsService(buf[:n], a.instance, a.host)
			if shouldReply {
				destination := mdnsGroup
				responseID := uint16(0)
				if source.Port != mdnsGroup.Port {
					destination = source
					responseID = id
				}
				a.sendResponse(responseID, mdnsTTL, destination)
			}
		}

		select {
		case <-a.stop:
			return
		default:
		}
		if time.Now().After(nextAnnouncement) {
			a.sendAnnouncement(mdnsTTL, mdnsGroup)
			nextAnnouncement = time.Now().Add(60 * time.Second)
		}
	}
}

func (a *Advertiser) sendAnnouncement(ttl uint32, destination *net.UDPAddr) {
	a.sendResponse(0, ttl, destination)
}

func (a *Advertiser) sendResponse(id uint16, ttl uint32, destination *net.UDPAddr) {
	packet := buildResponse(id, ttl, a.instance, a.host, a.port, a.ip, a.txt)
	a.writeMutex.Lock()
	_, _ = a.conn.WriteToUDP(packet, destination)
	a.writeMutex.Unlock()
}

func buildResponse(id uint16, ttl uint32, instance, host string, port int, ip net.IP, txt []string) []byte {
	packet := make([]byte, 12)
	binary.BigEndian.PutUint16(packet[0:2], id)
	binary.BigEndian.PutUint16(packet[2:4], 0x8400) // response + authoritative
	binary.BigEndian.PutUint16(packet[6:8], 4)

	packet = appendRecord(packet, ServiceType, dnsTypePTR, 1, ttl, encodeName(instance))
	srvData := []byte{0, 0, 0, 0, byte(port >> 8), byte(port)}
	srvData = append(srvData, encodeName(host)...)
	packet = appendRecord(packet, instance, dnsTypeSRV, 0x8001, ttl, srvData)
	packet = appendRecord(packet, instance, dnsTypeTXT, 0x8001, ttl, encodeTXT(txt))
	packet = appendRecord(packet, host, dnsTypeA, 0x8001, ttl, []byte(ip.To4()))
	return packet
}

func appendRecord(packet []byte, name string, recordType, class uint16, ttl uint32, data []byte) []byte {
	packet = append(packet, encodeName(name)...)
	header := make([]byte, 10)
	binary.BigEndian.PutUint16(header[0:2], recordType)
	binary.BigEndian.PutUint16(header[2:4], class)
	binary.BigEndian.PutUint32(header[4:8], ttl)
	binary.BigEndian.PutUint16(header[8:10], uint16(len(data)))
	packet = append(packet, header...)
	return append(packet, data...)
}

func encodeName(name string) []byte {
	name = strings.TrimSuffix(name, ".")
	var encoded []byte
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 {
			label = label[:63]
		}
		encoded = append(encoded, byte(len(label)))
		encoded = append(encoded, label...)
	}
	return append(encoded, 0)
}

func encodeTXT(values []string) []byte {
	var encoded []byte
	for _, value := range values {
		if len(value) > 255 {
			value = value[:255]
		}
		encoded = append(encoded, byte(len(value)))
		encoded = append(encoded, value...)
	}
	return encoded
}

func safeDNSLabel(value string) string {
	value = strings.TrimSpace(value)
	var label strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			label.WriteRune(r)
		case r == ' ' || r == '_' || r == '.':
			label.WriteByte('-')
		}
		if label.Len() >= 50 {
			break
		}
	}
	result := strings.Trim(label.String(), "-")
	if result == "" {
		return "landrop"
	}
	return result
}

func queryRequestsService(packet []byte, instance, host string) (uint16, bool) {
	if len(packet) < 12 || binary.BigEndian.Uint16(packet[2:4])&0x8000 != 0 {
		return 0, false
	}
	id := binary.BigEndian.Uint16(packet[0:2])
	questions := int(binary.BigEndian.Uint16(packet[4:6]))
	offset := 12
	for i := 0; i < questions; i++ {
		name, next, err := decodeName(packet, offset, 0)
		if err != nil || next+4 > len(packet) {
			return id, false
		}
		recordType := binary.BigEndian.Uint16(packet[next : next+2])
		offset = next + 4
		canonical := strings.ToLower(strings.TrimSuffix(name, ".") + ".")
		matchesName := canonical == strings.ToLower(ServiceType) ||
			canonical == strings.ToLower(instance) || canonical == strings.ToLower(host)
		matchesType := recordType == dnsTypeANY || recordType == dnsTypePTR || recordType == dnsTypeSRV ||
			recordType == dnsTypeTXT || recordType == dnsTypeA
		if matchesName && matchesType {
			return id, true
		}
	}
	return id, false
}

func decodeName(packet []byte, offset, depth int) (string, int, error) {
	if depth > 16 || offset < 0 || offset >= len(packet) {
		return "", 0, errors.New("invalid DNS name")
	}
	labels := make([]string, 0, 4)
	next := offset
	jumped := false
	for {
		if offset >= len(packet) {
			return "", 0, errors.New("truncated DNS name")
		}
		length := int(packet[offset])
		if length&0xc0 == 0xc0 {
			if offset+1 >= len(packet) {
				return "", 0, errors.New("truncated DNS pointer")
			}
			pointer := int(binary.BigEndian.Uint16(packet[offset:offset+2]) & 0x3fff)
			suffix, _, err := decodeName(packet, pointer, depth+1)
			if err != nil {
				return "", 0, err
			}
			labels = append(labels, strings.TrimSuffix(suffix, "."))
			if !jumped {
				next = offset + 2
			}
			break
		}
		offset++
		if length == 0 {
			if !jumped {
				next = offset
			}
			break
		}
		if length > 63 || offset+length > len(packet) {
			return "", 0, errors.New("invalid DNS label")
		}
		labels = append(labels, string(packet[offset:offset+length]))
		offset += length
		if !jumped {
			next = offset
		}
	}
	return strings.Join(labels, ".") + ".", next, nil
}
