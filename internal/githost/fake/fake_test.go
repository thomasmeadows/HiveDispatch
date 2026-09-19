package fake

import (
	"context"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

func TestOpenThenFind(t *testing.T) {
	h := New()
	ctx := context.Background()
	if pr, err := h.FindPR(ctx, "o/r", "hive/HIVE-1"); err != nil || pr != nil {
		t.Fatalf("find before open: pr=%v err=%v", pr, err)
	}
	pr, err := h.OpenPR(ctx, "o/r", githost.Request{Title: "t", Head: "hive/HIVE-1", Base: "main"})
	if err != nil || pr == nil || pr.URL == "" || pr.Number != 1 {
		t.Fatalf("open: pr=%+v err=%v", pr, err)
	}
	again, _ := h.FindPR(ctx, "o/r", "hive/HIVE-1")
	if again == nil || again.URL != pr.URL {
		t.Errorf("find after open = %+v", again)
	}
	if len(h.Opened()) != 1 {
		t.Errorf("opened = %+v", h.Opened())
	}
}
