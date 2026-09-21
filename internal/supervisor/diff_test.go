package supervisor

import "testing"

func TestDiff(t *testing.T) {
	got := Diff("c.yaml", "a\nb\nc\n", "a\nB\nc\nd\n")
	want := "--- c.yaml\n+++ c.yaml\n a\n-b\n+B\n c\n+d\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := Diff("x", "", "one\n"); got != "--- x (missing)\n+++ x\n+one\n" {
		t.Errorf("new file diff = %q", got)
	}
}
