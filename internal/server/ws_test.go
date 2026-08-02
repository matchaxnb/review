package server

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialHub starts a hub behind a test server and returns a dialer for it.
func dialHub(t *testing.T) (*Hub, func() *websocket.Conn, func()) {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleWebSocket))

	dial := func() *websocket.Conn {
		t.Helper()
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	return hub, dial, srv.Close
}

// TestDisconnectLeavesNothingBehind verifies that a client going away releases
// both its hub registration and the goroutines serving it.
func TestDisconnectLeavesNothingBehind(t *testing.T) {
	hub, dial, closeSrv := dialHub(t)
	defer closeSrv()

	time.Sleep(100 * time.Millisecond)
	before := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		conn := dial()
		time.Sleep(50 * time.Millisecond)
		conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	hub.mu.RLock()
	clients := len(hub.clients)
	hub.mu.RUnlock()
	if clients != 0 {
		t.Errorf("expected no clients left, got %d", clients)
	}

	if leaked := runtime.NumGoroutine() - before; leaked > 1 {
		t.Errorf("%d goroutines left behind by 5 disconnects", leaked)
	}
}

// TestBroadcastReachesClient covers the ordinary delivery path.
func TestBroadcastReachesClient(t *testing.T) {
	hub, dial, closeSrv := dialHub(t)
	defer closeSrv()

	conn := dial()
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)

	hub.Broadcast(map[string]string{"type": "hello"})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"hello"`) {
		t.Errorf("unexpected message: %s", data)
	}
}
