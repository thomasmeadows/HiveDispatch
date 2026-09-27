package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// Vite is the Vite dev server behind `hivedispatch website -dev`, run as a
// child process on a private loopback port. The Go server stays the front
// door: it proxies every non-API request to Vite, the HMR websocket
// included, so the host and origin checks (and any future login gate) apply
// in development exactly as in production.
type Vite struct {
	URL  *url.URL
	cmd  *exec.Cmd
	done chan struct{}
	err  error // why the process exited; read after done is closed
}

// viteStartTimeout bounds the wait for Vite to accept connections.
var viteStartTimeout = 60 * time.Second

// StartVite runs dir/node_modules/.bin/vite on a free loopback port and
// waits until it accepts connections. Cancelling ctx stops it.
func StartVite(ctx context.Context, dir string, stderr io.Writer) (*Vite, error) {
	bin := filepath.Join(dir, "node_modules", ".bin", "vite")
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("no Vite in %s: run `npm ci` in %s first", filepath.Dir(bin), dir)
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	u := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	// Vite's own banner would advertise its private port; warnings and
	// compile errors still come through (and show in the browser overlay).
	cmd := exec.CommandContext(ctx, bin, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--strictPort", "--logLevel", "warn", "--clearScreen", "false")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = stderr, stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start vite: %w", err)
	}
	v := &Vite{URL: u, cmd: cmd, done: make(chan struct{})}
	go func() {
		v.err = cmd.Wait()
		close(v.done)
	}()
	deadline := time.Now().Add(viteStartTimeout)
	for {
		conn, err := net.DialTimeout("tcp", u.Host, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close() // only probing that it listens
			return v, nil
		}
		select {
		case <-v.done:
			return nil, fmt.Errorf("vite exited before it was ready: %w", v.err)
		case <-ctx.Done():
			<-v.done
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = cmd.Cancel() // give up; Wait reaps it
			<-v.done
			return nil, fmt.Errorf("vite did not start listening on %s within %s", u.Host, viteStartTimeout)
		}
	}
}

// Done is closed when the Vite process exits.
func (v *Vite) Done() <-chan struct{} { return v.done }

// Err is why Vite exited; valid once Done is closed.
func (v *Vite) Err() error { return v.err }

// Handler proxies requests, websocket upgrades included, to Vite.
func (v *Vite) Handler() http.Handler {
	p := httputil.NewSingleHostReverseProxy(v.URL)
	p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		select {
		case <-v.done:
			err = errors.New("the Vite dev server has stopped; restart `hivedispatch website -dev`")
		default:
		}
		http.Error(w, "vite: "+err.Error(), http.StatusBadGateway)
	}
	return p
}

// freePort asks the kernel for an unused loopback port.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return port, ln.Close()
}
