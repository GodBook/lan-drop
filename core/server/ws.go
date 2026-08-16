package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	wsGUID            = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxWSMessageSize  = 1 << 20          // hard cap for a single WebSocket message (1MB)
	wsPingInterval    = 30 * time.Second // server keepalive period
	wsReadIdleTimeout = 90 * time.Second // drop connections silent longer than this
	wsWriteTimeout    = 10 * time.Second // per-frame write deadline
)

type WSClient struct {
	conn    net.Conn
	bufr    *bufio.Reader
	send    chan []byte
	hub     *WSHub
	closed  bool
	mu      sync.Mutex
	writeMu sync.Mutex // serializes raw frame writes between read/write pumps
}

type WSHub struct {
	clients    map[*WSClient]bool
	broadcast  chan []byte
	register   chan *WSClient
	unregister chan *WSClient
	done       chan struct{}
	stopOnce   sync.Once
	mu         sync.RWMutex
	cfg        *Config
}

func NewWSHub(cfg *Config) *WSHub {
	return &WSHub{
		clients:    make(map[*WSClient]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *WSClient),
		unregister: make(chan *WSClient),
		done:       make(chan struct{}),
		cfg:        cfg,
	}
}

func (h *WSHub) Run() {
	for {
		select {
		case <-h.done:
			h.mu.Lock()
			for client := range h.clients {
				delete(h.clients, client)
				client.Close()
			}
			h.mu.Unlock()
			return

		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()

			// Send current history upon connect
			feed := h.cfg.GetTextFeed()
			initPayload, _ := json.Marshal(map[string]interface{}{
				"type": "init_feed",
				"data": feed,
			})
			select {
			case client.send <- initPayload:
			default:
			}

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.Close()
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			h.mu.Lock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					// Client buffer full: treat as dead
					delete(h.clients, client)
					client.Close()
				}
			}
			h.mu.Unlock()
		}
	}
}

// Shutdown terminates the hub loop and closes every live client connection.
func (h *WSHub) Shutdown() {
	h.stopOnce.Do(func() { close(h.done) })
}

func (h *WSHub) BroadcastJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err == nil {
		select {
		case h.broadcast <- data:
		case <-h.done:
		}
	}
}

func (c *WSClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.send)
		_ = c.conn.Close()
	}
}

// writeFrame serializes all raw writes (data frames, pings, pongs) so concurrent
// pumps can never interleave bytes on the wire, and applies a write deadline.
func (c *WSClient) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return writeWSFrame(c.conn, opcode, payload)
}

func (c *WSClient) writePump() {
	defer c.hub.unregisterClient(c)
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				_ = c.writeFrame(0x8, []byte{})
				return
			}
			if err := c.writeFrame(0x1, message); err != nil { // Text frame
				return
			}
		case <-ticker.C:
			// Keepalive ping; browsers answer automatically at protocol level
			if err := c.writeFrame(0x9, []byte("landrop-ping")); err != nil {
				return
			}
		}
	}
}

func (c *WSClient) readPump() {
	defer c.hub.unregisterClient(c)
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(wsReadIdleTimeout))
		opcode, payload, err := readWSFrame(c.bufr, maxWSMessageSize)
		if err != nil {
			return
		}
		switch opcode {
		case 0x8: // Close frame
			return
		case 0x9: // Ping -> Pong
			if err := c.writeFrame(0xA, payload); err != nil {
				return
			}
		case 0xA: // Pong: keepalive answer, nothing to do
		case 0x1: // Text frame
			var req map[string]interface{}
			if err := json.Unmarshal(payload, &req); err == nil {
				msgType, _ := req["type"].(string)
				if msgType == "send_text" {
					content, _ := req["content"].(string)
					sender, _ := req["sender"].(string)
					if strings.TrimSpace(content) != "" {
						if sender == "" {
							sender = "Device"
						}
						msg := TextMessage{
							ID:        time.Now().Format("20060102150405.000"),
							Content:   content,
							Sender:    sender,
							Timestamp: time.Now(),
						}
						c.hub.cfg.AddTextMessage(msg)
						c.hub.BroadcastJSON(map[string]interface{}{
							"type": "new_text",
							"data": msg,
						})
					}
				}
			}
		}
	}
}

func (h *WSHub) unregisterClient(c *WSClient) {
	select {
	case h.unregister <- c:
	case <-h.done:
	}
}

// ServeWebSocket upgrades HTTP request to RFC-6455 WebSocket
func (h *WSHub) ServeWebSocket(w http.ResponseWriter, r *http.Request) {
	// Cross-Site WebSocket Hijacking guard: when a browser supplies an Origin,
	// it must point at this host. Non-browser clients send no Origin and pass.
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return
		}
	}

	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "Expected websocket upgrade", http.StatusBadRequest)
		return
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "Missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}

	// Compute accept hash
	hash := sha1.New()
	hash.Write([]byte(key + wsGUID))
	acceptKey := base64.StdEncoding.EncodeToString(hash.Sum(nil))

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Webserver doesn't support hijacking", http.StatusInternalServerError)
		return
	}

	conn, bufrw, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Write handshake response
	handshake := fmt.Sprintf("HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: %s\r\n\r\n", acceptKey)
	if _, err := conn.Write([]byte(handshake)); err != nil {
		_ = conn.Close()
		return
	}

	client := &WSClient{
		conn: conn,
		bufr: bufrw.Reader,
		send: make(chan []byte, 64),
		hub:  h,
	}

	select {
	case h.register <- client:
	case <-h.done:
		_ = conn.Close()
		return
	}

	go client.writePump()
	go client.readPump()
}

func writeWSFrame(w io.Writer, opcode byte, payload []byte) error {
	length := len(payload)
	var header []byte
	header = append(header, 0x80|opcode) // FIN + Opcode

	if length <= 125 {
		header = append(header, byte(length))
	} else if length <= 65535 {
		header = append(header, 126)
		lenBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(lenBytes, uint16(length))
		header = append(header, lenBytes...)
	} else {
		header = append(header, 127)
		lenBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(lenBytes, uint64(length))
		header = append(header, lenBytes...)
	}

	if _, err := w.Write(header); err != nil {
		return err
	}
	if length > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// readWSFrame reads one frame, refusing lengths above maxLen so a malformed or
// hostile peer can never make the server allocate unbounded memory.
func readWSFrame(r io.Reader, maxLen int) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}

	opcode := header[0] & 0x0F
	isMasked := (header[1] & 0x80) != 0
	payloadLen := int64(header[1] & 0x7F)

	if payloadLen == 126 {
		lenBytes := make([]byte, 2)
		if _, err := io.ReadFull(r, lenBytes); err != nil {
			return 0, nil, err
		}
		payloadLen = int64(binary.BigEndian.Uint16(lenBytes))
	} else if payloadLen == 127 {
		lenBytes := make([]byte, 8)
		if _, err := io.ReadFull(r, lenBytes); err != nil {
			return 0, nil, err
		}
		u := binary.BigEndian.Uint64(lenBytes)
		if u > uint64(math.MaxInt64) {
			return 0, nil, fmt.Errorf("websocket frame length overflow: %d", u)
		}
		payloadLen = int64(u)
	}

	if payloadLen > int64(maxLen) {
		return 0, nil, fmt.Errorf("websocket frame too large: %d bytes (max %d)", payloadLen, maxLen)
	}

	var maskKey []byte
	if isMasked {
		maskKey = make([]byte, 4)
		if _, err := io.ReadFull(r, maskKey); err != nil {
			return 0, nil, err
		}
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}

	if isMasked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	return opcode, payload, nil
}
