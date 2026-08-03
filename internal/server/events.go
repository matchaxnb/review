package server

import (
	"review/internal/store"
	"review/internal/watcher"
)

// serverEvent is a message a client receives when something changed. It carries
// the annotation state needed to apply the change, so a client does not have to
// ask for it separately.
type serverEvent struct {
	Type           string                                   `json:"type"`
	Path           string                                   `json:"path,omitempty"`
	Annotations    map[string]annotationResponse            `json:"annotations,omitempty"`
	AllAnnotations map[string]map[string]annotationResponse `json:"allAnnotations,omitempty"`
}

// BridgeWatcher connects a file watcher to the WebSocket hub: clients register
// the file they are looking at, and changes on disk are sent back out to them.
// It returns once the bridge is running.
func BridgeWatcher(hub *Hub, st *store.Store, w *watcher.Watcher) {
	// Let clients register the file to watch, which is the one they display
	hub.handle(func(msg clientMessage) {
		if msg.Type == "watch-file" && msg.Path != "" {
			w.WatchFile(msg.Path)
		}
	})

	go func() {
		for ev := range w.Events() {
			out := serverEvent{Type: ev.Type, Path: ev.Path}
			switch ev.Type {
			case "file-changed":
				out.Annotations = fileAnnotations(st.GetFile(ev.Path))
			case "review-reloaded":
				out.AllAnnotations = allAnnotations(st.All())
			}
			hub.Broadcast(out)
		}
	}()
}

// Shutdown tells clients that the server is going away, so they can close the
// tab it was opened in.
func (h *Hub) Shutdown() {
	h.Broadcast(serverEvent{Type: "server-shutdown"})
}
