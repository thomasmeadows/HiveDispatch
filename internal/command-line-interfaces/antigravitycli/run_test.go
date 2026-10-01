package antigravitycli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOversizedOutputStopsProcess(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\nhead -c 1100000 /dev/zero | tr '\\000' 'x'\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, exit, _, err := Run(ctx, Cmd{Binary: bin})
	if err != nil {
		t.Fatal(err)
	}
	if exit.ParseErr == nil || ctx.Err() != nil {
		t.Fatalf("parse error %v, context %v", exit.ParseErr, ctx.Err())
	}
}

func TestBoundedBufferConsumesFullWrite(t *testing.T) {
	b := &boundedBuffer{n: 10}
	for range 2 {
		n, err := b.Write([]byte(strings.Repeat("x", 20)))
		if n != 20 || err != nil {
			t.Fatalf("write %d, %v", n, err)
		}
	}
	if b.String() != strings.Repeat("x", 10) {
		t.Fatalf("buffer %q", b.String())
	}
}

func TestRunRetainsDiagnosticsAndBoundsLogs(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "agy")
	script := `#!/bin/sh
printf 'permission notice\n' >&2
printf '{"event":"result","result":{"status":"SUCCESS","response":"Done"}}\n'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	tr, exit, log, err := Run(context.Background(), Cmd{Binary: bin, MaxLog: 20})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Result == nil || exit.ExitErr != nil || !strings.Contains(exit.Stderr, "permission notice") || len(log) > 20 {
		t.Fatalf("exit %+v, log %q", exit, log)
	}
}

func TestNormalExitClosesBackgroundPipes(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "agy")
	script := `#!/bin/sh
sleep 30 &
printf '{"event":"result","result":{"status":"SUCCESS","response":"Done"}}\n'
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tr, exit, _, err := Run(ctx, Cmd{Binary: bin})
	if err != nil {
		t.Fatal(err)
	}
	if exit.CtxErr != nil || exit.ExitErr != nil || exit.ParseErr != nil || tr.Result == nil {
		t.Fatalf("exit %+v, result %+v", exit, tr.Result)
	}
}
