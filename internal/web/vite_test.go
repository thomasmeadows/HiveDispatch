package web

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for Vite: with HIVE_FAKE_VITE set
// it serves a page and an echoing websocket on --port, like `vite` would.
func TestMain(m *testing.M) {
	switch os.Getenv("HIVE_FAKE_VITE") {
	case "":
		os.Exit(m.Run())
	case "fail":
		fmt.Fprintln(os.Stderr, "error: config broken")
		os.Exit(1)
	default:
		fakeVite()
	}
}

func fakeVite() {
	fs := flag.NewFlagSet("vite", flag.ExitOnError)
	host := fs.String("host", "", "")
	port := fs.String("port", "", "")
	fs.Bool("strictPort", false, "")
	fs.String("logLevel", "", "")
	fs.String("clearScreen", "", "")
	_ = fs.Parse(os.Args[1:]) // ExitOnError
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			echoUpgrade(w)
			return
		}
		fmt.Fprintf(w, "vite dev page for host %s", r.Host)
	})
	if err := http.ListenAndServe(net.JoinHostPort(*host, *port), mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// echoUpgrade answers an upgrade with 101 and then echoes raw bytes — enough
// to show the proxy carries the HMR socket through.
func echoUpgrade(w http.ResponseWriter) {
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	_ = rw.Flush()
	_, _ = io.Copy(conn, rw)
}

// fakeViteDir makes a web/ directory whose node_modules/.bin/vite runs this
// test binary as fakeVite in mode.
func fakeViteDir(t *testing.T, mode string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "node_modules", ".bin")
	mustMkdir(t, bin)
	body := fmt.Sprintf("#!/bin/sh\nHIVE_FAKE_VITE=%s exec %q \"$@\"\n", mode, exe)
	if err := os.WriteFile(filepath.Join(bin, "vite"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestViteProxiesPagesAndHMRSocket(t *testing.T) {
	script(t, "") // skips on Windows
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v, err := StartVite(ctx, fakeViteDir(t, "on"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Frontend: v.Handler()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/src/App.vue")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	if want := "vite dev page for host " + host; string(body) != want {
		t.Errorf("proxied page = %q, want %q (the browser's Host must reach Vite)", body, want)
	}
	var st chatStatus
	if code := (&env{srv: srv}).get(t, "/api/chat", &st); code != 200 {
		t.Errorf("/api stays with Go: %d", code)
	}

	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /?token=x HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", host)
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("upgrade status = %q, %v", status, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	fmt.Fprint(conn, "hmr-ping")
	got := make([]byte, len("hmr-ping"))
	if _, err := io.ReadFull(br, got); err != nil || string(got) != "hmr-ping" {
		t.Errorf("echo through the proxy = %q, %v", got, err)
	}

	cancel()
	select {
	case <-v.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("vite was not stopped by cancelling its context")
	}
}

func TestViteStartFailures(t *testing.T) {
	script(t, "")
	if _, err := StartVite(context.Background(), t.TempDir(), io.Discard); err == nil || !strings.Contains(err.Error(), "npm ci") {
		t.Errorf("no node_modules: %v", err)
	}
	var out strings.Builder
	_, err := StartVite(context.Background(), fakeViteDir(t, "fail"), &out)
	if err == nil || !strings.Contains(err.Error(), "exited before it was ready") || !strings.Contains(out.String(), "config broken") {
		t.Errorf("failing vite: %v (output %q)", err, out.String())
	}
}
