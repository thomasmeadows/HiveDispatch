package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor"
	"github.com/thomasmeadows/hivedispatch/internal/web"
)

// runWebsite serves the local operator UI until interrupted.
func runWebsite(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("website", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "path to worker config")
	addr := fs.String("addr", "127.0.0.1:7878", "address to listen on")
	open := fs.Bool("open", false, "open the site in the default browser")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveWebsite(ctx, ln, *cfgPath, *open, stdout, stderr)
}

// serveWebsite serves on ln until ctx is done.
func serveWebsite(ctx context.Context, ln net.Listener, cfgPath string, openBrowser bool, stdout, stderr io.Writer) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var allow []string
	if host, _, err := net.SplitHostPort(ln.Addr().String()); err == nil {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			fmt.Fprintf(stderr, "warning: listening on %s, not loopback — anyone who can reach it can edit your config.\n", ln.Addr())
			if ip == nil || !ip.IsUnspecified() {
				allow = append(allow, host)
			}
		}
	}
	srv, err := web.New(web.Options{
		ConfigPath: cfgPath, Exe: exe, AllowHosts: allow,
		ListRuns: func(ctx context.Context) ([]state.Run, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			_, store, err := openStores(ctx, cfg)
			if err != nil {
				return nil, err
			}
			return store.List(ctx)
		},
		Check: func(ctx context.Context) string { return supervisor.CheckOutput(ctx, exe, cfgPath) },
		NewChat: func(ctx context.Context, confirm func(string) bool, events func(supervisor.Event)) (*supervisor.Session, error) {
			return supervisor.NewSession(ctx, supervisor.SessionOptions{
				WorkerConfigPath: cfgPath, Exe: exe, Confirm: confirm, Events: events,
			})
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer srv.Close()
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	url := "http://" + displayAddr(ln.Addr())
	fmt.Fprintf(stdout, "HiveDispatch website on %s (config %s) — Ctrl-C to stop\n", url, cfgPath)
	if openBrowser {
		if err := launchBrowser(url); err != nil {
			fmt.Fprintln(stderr, "could not open a browser:", err)
		}
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		fmt.Fprintln(stderr, err)
		return 1
	case <-ctx.Done():
	}
	srv.Close() // end a supervisor turn in flight, so its event stream closes
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// displayAddr is addr as a browser should use it: a wildcard bind is
// reached through localhost, which the server answers.
func displayAddr(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsUnspecified() || ip.IsLoopback()) {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

// launchBrowser opens url with the platform's opener.
func launchBrowser(url string) error {
	name := "xdg-open"
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name = "explorer"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap the opener; its exit status says nothing useful
	return nil
}
