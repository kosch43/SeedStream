package torrent

import (
	"testing"
	"time"
)

// Brute-force check of the byte->piece mapping via the cached path only
// (lastFetch=now so it never calls a client).
func TestPieceMathBounds(t *testing.T) {
	newA := func(states []int) *fileAvailability {
		return &fileAvailability{
			pieceSize: 1000, firstPiece: 5, lastPiece: 8, fileSize: 3500,
			pieceMode: true, aligned: true, states: states, lastFetch: time.Now(),
		}
	}
	all := []int{0, 0, 0, 0, 0, 2, 2, 2, 2} // pieces 5..8 downloaded

	for _, c := range []struct {
		off, length int64
		want        bool
	}{
		{0, 1, true}, {999, 1, true}, {1000, 1, true}, {3499, 1, true}, {0, 3500, true},
	} {
		if got := newA(all).piecesAvailableLocked(nil, c.off, c.off+c.length); got != c.want {
			t.Errorf("all-downloaded off=%d len=%d: got %v want %v", c.off, c.length, got, c.want)
		}
	}

	missing7 := []int{0, 0, 0, 0, 0, 2, 2, 0, 2} // piece 7 missing
	for _, c := range []struct {
		off, length int64
		want        bool
	}{
		{2000, 1, false},   // start of piece 7
		{2999, 1, false},   // end of piece 7
		{1500, 600, false}, // piece 6 spanning into 7
		{500, 100, true},   // fully in piece 5
		{1000, 500, true},  // fully in piece 6
		{3000, 500, true},  // fully in piece 8
	} {
		if got := newA(missing7).piecesAvailableLocked(nil, c.off, c.off+c.length); got != c.want {
			t.Errorf("missing-7 off=%d len=%d: got %v want %v", c.off, c.length, got, c.want)
		}
	}
}

// TestUnalignedNeverApprovesMissing brute-forces the unaligned mapping against
// ground truth: for a file starting mid-piece, no byte whose true piece is
// missing may ever be reported available. Over-conservatism (blocking an
// available byte) is allowed; approving a missing byte is not.
func TestUnalignedNeverApprovesMissing(t *testing.T) {
	const pieceSize = 1000
	const fileStartInTorrent = 500 // mid piece 0 -> unaligned
	const fileSize = 2000
	firstPiece := fileStartInTorrent / pieceSize                 // 0
	lastPiece := (fileStartInTorrent + fileSize - 1) / pieceSize // 2
	truePieceOf := func(fileByte int) int { return (fileStartInTorrent + fileByte) / pieceSize }

	// Try every possible single missing piece.
	for missing := firstPiece; missing <= lastPiece; missing++ {
		states := make([]int, lastPiece+1)
		for i := firstPiece; i <= lastPiece; i++ {
			states[i] = 2
		}
		states[missing] = 0
		a := &fileAvailability{
			pieceSize: pieceSize, firstPiece: firstPiece, lastPiece: lastPiece,
			fileSize: fileSize, pieceMode: true, aligned: false,
			states: states, lastFetch: time.Now(),
		}
		for b := 0; b < fileSize; b++ {
			got := a.piecesAvailableLocked(nil, int64(b), int64(b+1))
			if truePieceOf(b) == missing && got {
				t.Fatalf("APPROVED MISSING: byte %d is in missing piece %d but reported available", b, missing)
			}
		}
	}
}

func TestPieceForByteUsesTorrentOffsetForUnalignedFiles(t *testing.T) {
	a := &fileAvailability{
		initDone:   true,
		pieceMode:  true,
		pieceSize:  1000,
		fileOffset: 500,
		firstPiece: 0,
		lastPiece:  2,
		fileSize:   2000,
	}

	for _, tc := range []struct {
		offset int64
		piece  int
	}{
		{0, 0},
		{499, 0},
		{500, 1},
		{1499, 1},
		{1500, 2},
		{1999, 2},
	} {
		got, ok := a.PieceForByte(nil, tc.offset)
		if !ok || got != tc.piece {
			t.Errorf("PieceForByte(%d) = %d, %v; want %d, true", tc.offset, got, ok, tc.piece)
		}
	}
}
