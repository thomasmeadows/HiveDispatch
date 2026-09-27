// Package web is the local operator UI behind `hivedispatch website`: a JSON
// API over the worker, supervisor and repository config files, the scan,
// the run records and a supervisor chat, plus the embedded Vue front end
// that uses it. It is meant for the operator's own machine: it answers only
// requests addressed to a loopback host, and refuses cross-site writes.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/thomasmeadows/hivedispatch/internal/state"
	"github.com/thomasmeadows/hivedispatch/internal/supervisor"
)

// Options wires the server. Everything that reaches beyond the config
// files — git, trackers, the model — comes in as a function so tests can
// replace it.
type Options struct {
	ConfigPath string // the worker config
	Exe        string // the hivedispatch binary, for init
	// ListRuns returns the run records (what `status` shows).
	ListRuns func(ctx context.Context) ([]state.Run, error)
	// Check returns what `hivedispatch check` prints.
	Check func(ctx context.Context) string
	// NewChat starts a supervisor session that asks confirm before any
	// change and reports progress to events.
	NewChat func(ctx context.Context, confirm func(string) bool, events func(supervisor.Event)) (*supervisor.Session, error)
	// AllowHosts are extra Host names to answer besides the loopback ones,
	// for an operator who deliberately binds a non-loopback address.
	AllowHosts []string
	// Assets is the front end; nil serves the embedded build.
	Assets fs.FS
	// Getenv reads the environment (supervisor API keys); nil is os.Getenv.
	Getenv func(string) string
}

// Server is the HTTP handler. Close ends a supervisor turn in flight.
type Server struct {
	o    Options
	mux  *http.ServeMux
	chat *chat
}

// New builds the server.
func New(o Options) (*Server, error) {
	if o.Assets == nil {
		sub, err := fs.Sub(dist, "dist")
		if err != nil {
			return nil, err
		}
		o.Assets = sub
	}
	s := &Server{o: o, mux: http.NewServeMux(), chat: newChat(o.NewChat)}
	s.routes()
	return s, nil
}

// Close cancels any supervisor turn in flight.
func (s *Server) Close() { s.chat.close() }

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/overview", s.overview)
	m.HandleFunc("GET /api/check", s.check)
	m.HandleFunc("GET /api/runs", s.runs)
	m.HandleFunc("GET /api/repos", s.repos)
	m.HandleFunc("POST /api/repos/enrol", s.enrol)
	m.HandleFunc("POST /api/repos/setup", s.setupRepo)
	m.HandleFunc("GET /api/files/{kind}", s.getFile)
	m.HandleFunc("POST /api/files/{kind}", s.postFile)
	m.HandleFunc("GET /api/chat", s.chatState)
	m.HandleFunc("GET /api/chat/events", s.chatEvents)
	m.HandleFunc("POST /api/chat", s.chatSend)
	m.HandleFunc("POST /api/chat/confirm", s.chatConfirm)
	m.HandleFunc("POST /api/chat/cancel", s.chatCancel)
	m.HandleFunc("POST /api/chat/reset", s.chatReset)
	m.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, errors.New("no such endpoint"))
	})
	m.Handle("/", http.FileServerFS(s.o.Assets))
}

// ServeHTTP applies the host and origin checks, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostAllowed(r.Host) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if err := sameOrigin(r); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

// hostAllowed rejects requests addressed to any name but loopback (or an
// AllowHosts entry), so a web page cannot reach the API by pointing its own
// DNS name at 127.0.0.1.
func (s *Server) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || slices.Contains(s.o.AllowHosts, host) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin refuses a state-changing request another site's page could
// have sent: it must carry JSON (which a cross-site form cannot send
// without a preflight) and, when the browser names its origin, that origin
// must be this server.
func sameOrigin(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		return errors.New("requests that change something must be application/json")
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return errors.New("cross-origin request refused")
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return errors.New("cross-site request refused")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v) // the client has gone if this fails
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// readJSON decodes the request body into v, rejecting unknown fields.
func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("bad request body: " + err.Error())
	}
	return nil
}
