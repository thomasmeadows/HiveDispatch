// Package statusline keeps one line at the bottom of a terminal that is
// rewritten in place, while ordinary output scrolls above it. Route the
// logger through Line so log lines never land in the middle of the status.
package statusline

import (
	"io"
	"sync"
)

// clear returns to column 0 and erases the whole line (ANSI EL 2).
const clear = "\r\x1b[2K"

// Line is a rewritable status line over a terminal writer.
type Line struct {
	mu   sync.Mutex
	w    io.Writer
	text string
}

// New returns a Line over w, which should be a terminal. Nothing is
// written until Set is called.
func New(w io.Writer) *Line {
	return &Line{w: w}
}

// Set replaces the status line with text. Text must not contain a newline.
func (l *Line) Set(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.text = text
	l.draw()
}

// Clear erases the status line and stops redrawing it after writes.
func (l *Line) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.text == "" {
		return
	}
	l.text = ""
	io.WriteString(l.w, clear)
}

// Write sends p to the terminal above the status line: the line is erased,
// p is written, and the line is redrawn. p should end in a newline.
func (l *Line) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.text != "" {
		io.WriteString(l.w, clear)
	}
	n, err := l.w.Write(p)
	if l.text != "" {
		l.draw()
	}
	return n, err
}

func (l *Line) draw() {
	io.WriteString(l.w, clear+l.text)
}
