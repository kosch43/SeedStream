package torrent

import (
	"context"
	"testing"

	"seedstream/pkg/torrent/tclient"
)

// anchorClient records SequentialFromPiece calls. It satisfies only the part of
// the interface the anchoring path touches.
type anchorClient struct {
	tclient.Client
	calls []int
	err   error
}

func (a *anchorClient) SequentialFromPiece(_ context.Context, _ string, piece int) error {
	if a.err != nil {
		return a.err
	}
	a.calls = append(a.calls, piece)
	return nil
}

// TestAnchorTargetsTheVideoFileNotTheTorrent is the point of anchoring. On a
// multi-file torrent the video can begin hundreds of pieces in, behind a sample
// or a subtitle folder that sorts earlier. Anchoring at the torrent's first
// piece would point the download at bytes no player will ever ask for first.
func TestAnchorTargetsTheVideoFileNotTheTorrent(t *testing.T) {
	c := &anchorClient{}
	var anchor sequentialAnchorer = c
	if err := anchor.SequentialFromPiece(context.Background(), "abc", 412); err != nil {
		t.Fatalf("SequentialFromPiece: %v", err)
	}
	if len(c.calls) != 1 || c.calls[0] != 412 {
		t.Fatalf("expected an anchor at the video's first piece, got %v", c.calls)
	}
}

// TestClientWithoutTheCapabilityIsSkipped: qBittorrent has no way to move the
// sequential start point, so the type assertion must simply not match rather
// than the prepare path assuming every client can be told.
func TestClientWithoutTheCapabilityIsSkipped(t *testing.T) {
	var plain tclient.Client = &anchorlessClient{}
	if _, ok := plain.(sequentialAnchorer); ok {
		t.Fatal("a client with no anchoring method must not satisfy the interface")
	}
}

type anchorlessClient struct{ tclient.Client }
