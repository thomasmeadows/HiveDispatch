package repolock

import "testing"

func TestForIsPerDirectory(t *testing.T) {
	a, b := For("/w/o__r"), For("/w/o__r/")
	if a != b {
		t.Error("the same directory must share one lock")
	}
	if For("/w/o__other") == a {
		t.Error("different repositories must not share a lock")
	}
}
