package none

import (
	"context"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/githost"
)

func TestNeverOpensOrFinds(t *testing.T) {
	h := Host{}
	if pr, err := h.FindPR(context.Background(), "o/r", "hive/X"); pr != nil || err != nil {
		t.Errorf("find = %v, %v", pr, err)
	}
	if pr, err := h.OpenPR(context.Background(), "o/r", githost.Request{}); pr != nil || err != nil {
		t.Errorf("open = %v, %v", pr, err)
	}
}
