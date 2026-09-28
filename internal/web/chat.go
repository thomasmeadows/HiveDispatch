package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/supervisor"
)

// chatEvent is one line of the chat sidebar, streamed over SSE and kept in
// the transcript so a reloaded page shows the conversation so far.
type chatEvent struct {
	Seq      int    `json:"seq"`
	Type     string `json:"type"` // user | reply | error | thinking | tool_start | tool_done | budget | confirm | confirm_done | reset
	Text     string `json:"text,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Args     string `json:"args,omitempty"`
	IsError  bool   `json:"is_error,omitempty"`
	ID       string `json:"id,omitempty"`
	Approved bool   `json:"approved,omitempty"`
	Busy     bool   `json:"busy,omitempty"` // thinking: a model call started (true) or ended (false)
}

// maxToolResult caps a tool result in the transcript; the model saw it all.
const maxToolResult = 4 << 10

var errBusy = errors.New("the supervisor is still answering; wait, or cancel it")

// chat is the one supervisor conversation the server holds (one operator,
// one browser). A turn runs on its own goroutine; confirmations it asks for
// wait on the browser's answer.
type chat struct {
	newSession func(ctx context.Context, confirm func(string) bool, events func(supervisor.Event)) (*supervisor.Session, error)
	base       context.Context
	stop       context.CancelFunc

	mu         sync.Mutex
	sess       *supervisor.Session
	sessErr    error
	busy       bool
	turnCtx    context.Context
	cancelTurn context.CancelFunc
	pending    map[string]chan bool
	seq        int
	log        []chatEvent
	subs       map[chan chatEvent]struct{}
	turns      sync.WaitGroup
}

func newChat(newSession func(context.Context, func(string) bool, func(supervisor.Event)) (*supervisor.Session, error)) *chat {
	base, stop := context.WithCancel(context.Background())
	return &chat{newSession: newSession, base: base, stop: stop, pending: map[string]chan bool{}, subs: map[chan chatEvent]struct{}{}}
}

// close cancels a turn in flight and waits for it to finish.
func (c *chat) close() {
	c.stop()
	c.turns.Wait()
}

// publish records e and sends it to every subscriber. Callers hold mu.
func (c *chat) publish(e chatEvent) {
	c.seq++
	e.Seq = c.seq
	c.log = append(c.log, e)
	for ch := range c.subs {
		select {
		case ch <- e:
		default: // a stalled browser misses the event; a reload replays the log
		}
	}
}

// ensure starts the session if there is none. Callers hold mu.
func (c *chat) ensure() {
	if c.sess != nil || c.sessErr != nil {
		return
	}
	if c.newSession == nil {
		c.sessErr = errors.New("the supervisor is not available")
		return
	}
	c.sess, c.sessErr = c.newSession(c.base, c.confirm, c.onEvent)
}

// onEvent forwards the agent's progress; it runs on the turn goroutine.
func (c *chat) onEvent(e supervisor.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch e.Kind {
	case "model_start", "model_done":
		c.publish(chatEvent{Type: "thinking", Busy: e.Kind == "model_start"})
	case "tool_start":
		c.publish(chatEvent{Type: "tool_start", Tool: e.Tool, Args: string(e.Args)})
	case "tool_done":
		res := e.Result
		if len(res) > maxToolResult {
			res = res[:maxToolResult] + "\n… (truncated)"
		}
		c.publish(chatEvent{Type: "tool_done", Tool: e.Tool, Text: res, IsError: e.Err != nil})
	case "budget":
		c.publish(chatEvent{Type: "budget"})
	}
}

// confirm asks the browser and waits for its answer; a cancelled turn (or
// a server shutting down) declines.
func (c *chat) confirm(prompt string) bool {
	c.mu.Lock()
	ctx := c.turnCtx
	if ctx == nil {
		c.mu.Unlock()
		return false
	}
	id := strconv.Itoa(c.seq + 1)
	ch := make(chan bool, 1)
	c.pending[id] = ch
	c.publish(chatEvent{Type: "confirm", ID: id, Text: prompt})
	c.mu.Unlock()
	ok := false
	select {
	case ok = <-ch:
	case <-ctx.Done():
	}
	c.mu.Lock()
	delete(c.pending, id)
	c.publish(chatEvent{Type: "confirm_done", ID: id, Approved: ok})
	c.mu.Unlock()
	return ok
}

// send starts a turn in the background.
func (c *chat) send(msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	if c.sess == nil {
		return c.sessErr
	}
	if c.busy {
		return errBusy
	}
	sess := c.sess
	ctx, cancel := context.WithCancel(c.base)
	c.busy, c.turnCtx, c.cancelTurn = true, ctx, cancel
	c.publish(chatEvent{Type: "user", Text: msg})
	c.turns.Add(1)
	go func() {
		defer c.turns.Done()
		defer cancel()
		sess.RefreshCheck(ctx)
		reply, err := sess.Turn(ctx, msg)
		var saveErr error
		if err == nil {
			saveErr = sess.Save()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.busy, c.turnCtx, c.cancelTurn = false, nil, nil
		switch {
		case errors.Is(err, context.Canceled):
			c.publish(chatEvent{Type: "error", Text: "interrupted"})
		case err != nil:
			c.publish(chatEvent{Type: "error", Text: err.Error()})
		default:
			c.publish(chatEvent{Type: "reply", Text: reply})
			if saveErr != nil {
				c.publish(chatEvent{Type: "error", Text: "session not saved: " + saveErr.Error()})
			}
		}
	}()
	return nil
}

func (c *chat) answer(id string, approve bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.pending[id]
	if !ok {
		return fmt.Errorf("no pending confirmation %q", id)
	}
	select {
	case ch <- approve:
	default: // already answered
	}
	return nil
}

func (c *chat) cancel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelTurn != nil {
		c.cancelTurn()
	}
}

// reset drops the conversation and starts a new session, re-reading the
// supervisor config (so an edited provider takes effect).
func (c *chat) reset() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy {
		return errBusy
	}
	c.sess, c.sessErr, c.log = nil, nil, nil
	c.publish(chatEvent{Type: "reset"})
	c.ensure()
	return nil
}

type chatStatus struct {
	Model   string      `json:"model,omitempty"`
	Session string      `json:"session,omitempty"`
	Error   string      `json:"error,omitempty"`
	Busy    bool        `json:"busy"`
	Notes   string      `json:"notes,omitempty"`
	Log     []chatEvent `json:"log"`
}

func (c *chat) status() chatStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	st := chatStatus{Busy: c.busy, Log: append([]chatEvent{}, c.log...)}
	if c.sessErr != nil {
		st.Error = c.sessErr.Error()
	}
	if c.sess != nil {
		st.Model, st.Session = c.sess.ModelName(), c.sess.Name()
		if n, err := c.sess.Notes(); err == nil {
			st.Notes = n
		}
	}
	return st
}

func (c *chat) subscribe() (ch chan chatEvent, unsubscribe func()) {
	ch = make(chan chatEvent, 64)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

func (s *Server) chatState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.chat.status())
}

// chatEvents streams chat events as server-sent events. The client loads
// GET /api/chat first and ignores events with a seq it already has.
func (s *Server) chatEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	ch, unsubscribe := s.chat.subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.chat.base.Done():
			return
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
		case e := <-ch:
			b, err := json.Marshal(e)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
		}
		flusher.Flush()
	}
}

func (s *Server) chatSend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message string `json:"message"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if in.Message == "" {
		writeError(w, http.StatusBadRequest, errors.New("message is empty"))
		return
	}
	if err := s.chat.send(in.Message); err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, errBusy) {
			code = http.StatusConflict
		}
		writeError(w, code, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) chatConfirm(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string `json:"id"`
		Approve bool   `json:"approve"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.chat.answer(in.ID, in.Approve); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) chatCancel(w http.ResponseWriter, _ *http.Request) {
	s.chat.cancel()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) chatReset(w http.ResponseWriter, _ *http.Request) {
	if err := s.chat.reset(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, s.chat.status())
}
