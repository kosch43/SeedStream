package stremio

import (
	"testing"

	"seedstream/pkg/auth"
)

// TestFailoverIsOffUnlessAskedFor: a release that cannot work is not swapped
// for a different one by default.
//
// Every fallback attempt adds another torrent to the seedbox, so a title whose
// first candidate fails quietly becomes several downloads for one film — none
// of which the viewer chose. Failing visibly leaves the choice where it
// started: the stream list is still in front of them.
func TestFailoverIsOffUnlessAskedFor(t *testing.T) {
	yes, no := true, false

	if streamFailoverEnabled(nil) {
		t.Error("no stream config at all must not enable failover")
	}
	if streamFailoverEnabled(&auth.Stream{}) {
		t.Error("an unset failover flag must read as off, not on")
	}
	if streamFailoverEnabled(&auth.Stream{EnableFailover: &no}) {
		t.Error("explicitly off must stay off")
	}
	if !streamFailoverEnabled(&auth.Stream{EnableFailover: &yes}) {
		t.Error("an operator who switched it on must get it")
	}
}
