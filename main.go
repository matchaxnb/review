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
	bindHost := flag.String("host", "127.0.0.1", "Address to listen on; 0.0.0.0 reaches the LAN and needs the access token printed at startup")
	httpHost := flag.String("http-host", "", "Hostname to use in the opening URL and browser redirect (default: derived from -host; a wildcard bind falls back to 127.0.0.1)")
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
	if !isLoopback(*bindHost) {
		token, err = server.NewToken(32)
		if err != nil {
			log.Fatalf("Failed to generate access token: %v", err)
		}
	}

	// The address to listen on and the host a browser should use are not the
	// same: a wildcard bind (0.0.0.0, ::) is not a reachable address, so the
	// browsable host is a real one, overridable for a name the client resolves.
	brHost := browserHost(*httpHost, *bindHost)
	handler := server.New(st, rootDir, baseCommit, subFS, hub, token)

	bindAddr := net.JoinHostPort(*bindHost, strconv.Itoa(*port))
	url := "http://" + net.JoinHostPort(brHost, strconv.Itoa(*port))
	launchURL := url
	if token != "" {
		launchURL = url + "/?token=" + token
	}
	fmt.Printf("Code Review running at %s\n", url)
	if *bindHost != brHost {
		fmt.Printf("Listening on %s\n", bindAddr)
	}
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
	} else {
		fmt.Printf("\nOpen %s in a browser.\n", url)
	}

	// Only try to open a browser when the address it would be opened at is this
	// machine's own: a review served for another machine has no local display to
	// open, and the URL is printed for the client instead.
	if isLoopback(brHost) {
		go openBrowser(launchURL)
	}

	srv := &http.Server{Addr: bindAddr, Handler: handler}

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

// browserHost returns the host a client should use to reach the server, given
// the optional -http-host override and the address the server binds. A wildcard
// bind (0.0.0.0 or ::) listens on every interface but is not itself a reachable
// address, so the machine's primary address is used instead; -http-host names it
// explicitly when that address is not the one a client should use.
func browserHost(httpHost, bindHost string) string {
	if httpHost != "" {
		return httpHost
	}
	if ip := net.ParseIP(bindHost); ip != nil && ip.IsUnspecified() {
		if primary := primaryIP(); primary != "" {
			return primary
		}
		return "127.0.0.1"
	}
	return bindHost
}

// primaryIP returns the machine's own address on the route it would use to leave
// the host, which is the one a client on the same network reaches it by. It
// returns an empty string when there is no such route (an isolated host).
func primaryIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return ""
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
