package server

import (
	"io/fs"
	"net/http"

	"review/internal/gitstatus"
	"review/internal/store"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// New creates and returns the HTTP handler.
// frontendFS should be the embedded frontend filesystem (already sub'd to the frontend root).
// base is the commit to compare against; its zero value compares against HEAD.
// hub may be nil if WebSocket support is not needed.
func New(st *store.Store, rootDir string, base gitstatus.Base, frontendFS fs.FS, hub *Hub) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	h := &handlers{
		store:   st,
		rootDir: rootDir,
		base:    base,
	}

	// Serve frontend
	fileServer := http.FileServer(http.FS(frontendFS))

	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		http.ServeFileFS(w, req, frontendFS, "index.html")
	})
	r.Handle("/static/*", http.StripPrefix("/static/", fileServer))

	// API routes
	r.Get("/api/tree", h.handleTree)
	r.Get("/api/file", h.handleFile)
	r.Get("/api/annotations", h.handleGetAnnotations)
	r.Post("/api/annotations", h.handleSetAnnotation)
	r.Delete("/api/annotations", h.handleDeleteAnnotation)
	r.Get("/api/git-status", h.handleGitStatus)
	r.Get("/api/config", h.handleConfig)
	r.Get("/api/chroma.css", h.handleChromaCSS)
	r.Delete("/api/review", h.handleDeleteReview)

	// WebSocket
	if hub != nil {
		r.Get("/ws", hub.HandleWebSocket)
	}

	return r
}
