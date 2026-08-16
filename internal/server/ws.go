package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type WSClient struct {
	conn   net.Conn
	bufr   *bufio.Reader
	send   chan []byte
	hub    *WSHub
	closed bool
	mu     sync.Mutex
}

type WSHub struct {
	clients    map[*WSClient]bool
	broadcast  chan []byte
	register   chan *WSClient
	unregister chan *WSClient
	mu         sync.RWMutex
	cfg        *Config
}

func NewWSHub(cfg *Config) *WSHub {
	return &WSHub{
		clients:    make(map[*WSClient]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *WSClient),
		unregister: make(chan *WSClient),
		cfg:        cfg,
	}
}

func (h *WSHub) Run() {
	for {
		select {
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
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					delete(h.clients, client)
					client.Close()
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *WSHub) BroadcastJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err == nil {
		h.broadcast <- data
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

func (c *WSClient) writePump() {
	defer c.hub.unregisterClient(c)
	for message := range c.send {
		err := writeWSFrame(c.conn, 0x1, message) // Text frame
		if err != nil {
			break
		}
	}
}

func (c *WSClient) readPump() {
	defer c.hub.unregisterClient(c)
	for {
		opcode, payload, err := readWSFrame(c.bufr)
		if err != nil {
			break
		}
		if opcode == 0x8 { // Close frame
			break
		}
		if opcode == 0x9 { // Ping frame
			_ = writeWSFrame(c.conn, 0xA, payload) // Pong
			continue
		}
		if opcode == 0x1 { // Text frame
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
						// Record and broadcast
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
	h.unregister <- c
}

// ServeWebSocket upgrades HTTP request to RFC-6455 WebSocket
func (h *WSHub) ServeWebSocket(w http.ResponseWriter, r *http.Request) {
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

	h.register <- client

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

func readWSFrame(r io.Reader) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}

	opcode := header[0] & 0x0F
	isMasked := (header[1] & 0x80) != 0
	payloadLen := int(header[1] & 0x7F)

	if payloadLen == 126 {
		lenBytes := make([]byte, 2)
		if _, err := io.ReadFull(r, lenBytes); err != nil {
			return 0, nil, err
		}
		payloadLen = int(binary.BigEndian.Uint16(lenBytes))
	} else if payloadLen == 127 {
		lenBytes := make([]byte, 8)
		if _, err := io.ReadFull(r, lenBytes); err != nil {
			return 0, nil, err
		}
		payloadLen = int(binary.BigEndian.Uint64(lenBytes))
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
		for i := 0; i < payloadLen; i++ {
			payload[i] ^= maskKey[i%4]
		}
	}

	return opcode, payload, nil
}
