package statusline

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

func TestSetRewritesInPlace(t *testing.T) {
	var out bytes.Buffer
	l := New(&out)
	l.Set("last poll 10:00:00")
	l.Set("last poll 10:00:05")
	want := clear + "last poll 10:00:00" + clear + "last poll 10:00:05"
	if out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
}

func TestWriteScrollsAboveTheStatusLine(t *testing.T) {
	var out bytes.Buffer
	l := New(&out)
	l.Set("status")
	n, err := fmt.Fprintln(l, "log line")
	if err != nil || n != len("log line\n") {
		t.Fatalf("n=%d err=%v", n, err)
	}
	want := clear + "status" + clear + "log line\n" + clear + "status"
	if out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
}

func TestWriteWithoutStatusPassesThrough(t *testing.T) {
	var out bytes.Buffer
	l := New(&out)
	fmt.Fprintln(l, "log line")
	if out.String() != "log line\n" {
		t.Errorf("out = %q", out.String())
	}
}

func TestClearErasesAndForgetsTheLine(t *testing.T) {
	var out bytes.Buffer
	l := New(&out)
	l.Set("status")
	l.Clear()
	fmt.Fprintln(l, "after")
	want := clear + "status" + clear + "after\n"
	if out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
}

func TestConcurrentUse(t *testing.T) {
	var out bytes.Buffer
	l := New(&out)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); l.Set("s") }()
		go func() { defer wg.Done(); fmt.Fprintln(l, "x") }()
	}
	wg.Wait()
	if !bytes.Contains(out.Bytes(), []byte("x\n")) {
		t.Error("log lines lost")
	}
}
