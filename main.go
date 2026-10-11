package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"review/internal/gitstatus"
	"review/internal/server"
	"review/internal/store"
	"review/internal/watcher"
)

//go:embed all:frontend
var frontendFS embed.FS

func main() {
	port := flag.Int("port", 7070, "HTTP server port")
	dir := flag.String("dir", ".", "Root directory to review")
	host := flag.String("host", "127.0.0.1", "Address to listen on; a non-loopback host needs the access token printed at startup")
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() > 1 {
		flag.Usage()
		os.Exit(2)
	}

	rootDir, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("Failed to resolve directory: %v", err)
	}

	var baseCommit gitstatus.Base
	if base := flag.Arg(0); base != "" {
		baseCommit, err = gitstatus.ResolveBase(rootDir, base)
		if err != nil {
			log.Fatalf("Failed to resolve base revision: %v", err)
		}
	}

	mdPath := filepath.Join(rootDir, "REVIEW.md")
	st, err := store.Load(mdPath, rootDir)
	if err != nil {
		log.Fatalf("Failed to load review data: %v", err)
	}

	// Run initial drift check on all annotated files
	drifted, err := st.CheckAllDrift()
	if err != nil {
		log.Printf("Warning: could not write adjusted annotations: %v", err)
	}
	for f := range drifted {
		log.Printf("Drift detected in %s — annotations adjusted", f)
	}

	subFS, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		log.Fatalf("Failed to create sub filesystem: %v", err)
	}

	// Set up WebSocket hub
	hub := server.NewHub()
	go hub.Run()

	// Set up file watcher
	if w, err := watcher.New(st); err != nil {
		log.Printf("Warning: file watching disabled: %v", err)
	} else {
		w.Start()
		defer w.Stop()
		server.BridgeWatcher(hub, st, w)
	}

	// A review exposes the whole source tree and has no access control of its
	// own, so listening beyond the loopback interface is guarded by an access
	// token rather than left open to the network.
	token := ""
	if !isLoopback(*host) {
		token, err = server.NewToken(32)
		if err != nil {
			log.Fatalf("Failed to generate access token: %v", err)
		}
	}

	handler := server.New(st, rootDir, baseCommit, subFS, hub, token)

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	url := "http://" + addr
	fmt.Printf("Code Review running at %s\n", url)
	fmt.Printf("Reviewing: %s\n", rootDir)
	if baseCommit.Commit != "" {
		fmt.Printf("Comparing against: %s (%s)\n", baseCommit.Rev, baseCommit.Commit[:7])
	}
	fmt.Printf("Annotations: %s\n", st.MdPath())

	if token != "" {
		fmt.Printf("\nListening beyond the loopback interface. Open this URL to\n")
		fmt.Printf("authorise your browser, then browse normally (the token is kept\n")
		fmt.Printf("in a cookie):\n\n  %s/?token=%s\n", url, token)
		fmt.Printf("\nAnyone with the token can read the reviewed sources.\n")
		go openBrowser(fmt.Sprintf("%s/?token=%s", url, token))
	} else {
		go openBrowser(url)
	}

	srv := &http.Server{Addr: addr, Handler: handler}

	// Graceful shutdown: catch signals, notify clients, then stop
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		hub.Shutdown()
		time.Sleep(200 * time.Millisecond) // give WS time to deliver
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

// isLoopback reports whether host names the local machine, so binding to it
// reaches only this machine and needs no access token. An empty host (all
// interfaces) is not loopback.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// usage prints the command line syntax, including the optional base revision
// that the flag package does not know about.
func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "Usage: %s [flags] [base]\n\n", filepath.Base(os.Args[0]))
	fmt.Fprint(out, "  base\n    \tBranch, tag or commit to compare against instead of HEAD\n")
	flag.PrintDefaults()
}

func openBrowser(url string) {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}

	args = append(args, url)
	exec.Command(cmd, args...).Start()
}
