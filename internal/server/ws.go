package server

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// pongWait is how long a client may stay silent before its connection is
	// treated as dead.
	pongWait = 60 * time.Second
	// pingPeriod is the interval between keepalive pings. It has to leave room
	// for the reply to arrive before pongWait expires.
	pingPeriod = 45 * time.Second
	// writeWait is the time allowed for writing a single message.
	writeWait = 10 * time.Second
	// readLimit bounds a message from a client. Clients only ever send the path
	// of the file they are looking at, so this leaves room for the longest path
	// the filesystem allows and nothing more.
	readLimit = 4096
)

// clientMessage is a message sent by a client.
type clientMessage struct {
	Type string `json:"type"` // "watch-file"
	Path string `json:"path"` // file the client is looking at
}

// upgrader accepts connections from the page the server itself serves. Any
// site a user visits could otherwise open a socket to the local review and
// read along.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // not a browser
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
}

// Hub maintains the set of active WebSocket clients and broadcasts messages.
type Hub struct {
	clients   map[*wsClient]bool
	mu        sync.RWMutex
	broadcast chan []byte
	onMessage func(msg clientMessage) // optional handler for client messages
}

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

// NewHub creates a new WebSocket hub.
func NewHub() *Hub {
	return &Hub{
		clients:   make(map[*wsClient]bool),
		broadcast: make(chan []byte, 64),
	}
}

// handle sets the handler for client→server messages.
func (h *Hub) handle(fn func(msg clientMessage)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onMessage = fn
}

// messageHandler returns the handler for client→server messages, or nil when
// none is set.
func (h *Hub) messageHandler() func(msg clientMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.onMessage
}

// Run starts the hub's broadcast loop.
func (h *Hub) Run() {
	for msg := range h.broadcast {
		var stalled []*wsClient
		h.mu.RLock()
		for client := range h.clients {
			select {
			case client.send <- msg:
			default:
				stalled = append(stalled, client)
			}
		}
		h.mu.RUnlock()
		// Drop clients that cannot keep up, outside the read lock
		for _, client := range stalled {
			h.remove(client)
		}
	}
}

// remove unregisters a client and closes its send channel, which lets its
// writer finish. Removing a client that is already gone does nothing.
func (h *Hub) remove(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c] {
		delete(h.clients, c)
		close(c.send)
	}
}

// Broadcast sends a JSON message to all connected clients.
func (h *Hub) Broadcast(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("ws broadcast marshal error: %v", err)
		return
	}
	select {
	case h.broadcast <- data:
	default:
		// Broadcast channel full
	}
}

// HandleWebSocket handles WebSocket upgrade requests.
func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}

	client := &wsClient{
		conn: conn,
		send: make(chan []byte, 32),
	}

	h.mu.Lock()
	h.clients[client] = true
	h.mu.Unlock()

	go client.writePump()
	go client.readPump(h)
}

// writePump delivers broadcasts to one client and keeps the connection alive
// by pinging it while there is nothing to send.
func (c *wsClient) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *wsClient) readPump(h *Hub) {
	defer func() {
		h.remove(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(readLimit)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		handler := h.messageHandler()
		if handler == nil {
			continue
		}
		var msg clientMessage
		if json.Unmarshal(data, &msg) == nil {
			handler(msg)
		}
	}
}
