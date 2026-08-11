package torrent

import "testing"

const testMiB = 1 << 20

// finalReckoningProfile is the field case: an 80 GB 4K remux, 2h49m, published
// with 64 MiB pieces. One piece is 7.9 seconds of video — less than the
// reader's own lowRunwaySeconds, so starting on one piece produces a stream
// that immediately warns it is about to stall.
func finalReckoningProfile() PlaybackProfile {
	return PlaybackProfile{FileBytes: 80 * 1024 * testMiB, RuntimeSeconds: 169 * 60}
}

func TestOnePieceIsNotEnoughRunwayOnHugePieces(t *testing.T) {
	p := finalReckoningProfile()
	const pieceSize = 64 * testMiB

	oneRunway := float64(pieceSize) / p.BytesPerSecond()
	if oneRunway >= lowRunwaySeconds {
		t.Fatalf("test premise broken: one piece is %.1fs, which the reader would not warn about", oneRunway)
	}

	// A download comfortably outrunning playback: the rate model asks for
	// jitterSeconds only, which is under one piece, so the floor decides.
	got := HeadBytesForPieceTracking(p, 60*testMiB, pieceSize)
	if got != 2*pieceSize {
		t.Errorf("head = %d (%d pieces), want 2 pieces: one piece is only %.1fs of video",
			got, got/pieceSize, oneRunway)
	}
	if runway := float64(got) / p.BytesPerSecond(); runway < lowRunwaySeconds {
		t.Errorf("the head SeedStream starts on covers %.1fs, which the reader reports as a likely stall", runway)
	}
}

// The floor is about time, not size. A 64 MiB piece on a modest 1080p release
// is nearly a minute of video, and doubling that would double a startup wait
// for a stream that was never near a cliff.
func TestLargePiecesOnLowBitrateAreLeftAlone(t *testing.T) {
	p := PlaybackProfile{FileBytes: 8 * 1024 * testMiB, RuntimeSeconds: 120 * 60}
	const pieceSize = 64 * testMiB

	if runway := float64(pieceSize) / p.BytesPerSecond(); runway < lowRunwaySeconds {
		t.Fatalf("test premise broken: one piece is only %.1fs here", runway)
	}
	if got := HeadBytesForPieceTracking(p, 60*testMiB, pieceSize); got != pieceSize {
		t.Errorf("head = %d (%d pieces), want exactly one: one piece is already ~56s of video",
			got, got/pieceSize)
	}
}

// The small-piece fast start is the thing most likely to be broken by a change
// here, so it is pinned: ordinary 4 MiB pieces must not gain a floor, because
// the byte-sized head already spans many of them.
func TestOrdinaryPieceSizesAreUnchanged(t *testing.T) {
	p := PlaybackProfile{FileBytes: 25 * 1024 * testMiB, RuntimeSeconds: 133 * 60}
	for _, pieceSize := range []int64{4 * testMiB, 8 * testMiB, 16 * testMiB} {
		withFloor := HeadBytesForPieceTracking(p, 60*testMiB, pieceSize)
		// What the same call produced before the floor existed: clamped with a
		// one-piece minimum.
		playback := p.BytesPerSecond()
		want := int64(float64(headSecondsFor(p, 60*testMiB)) * playback)
		baseline := clampHeadTo(want, p.FileBytes, pieceSize)
		if withFloor < baseline {
			t.Errorf("piece %d MiB: floor lowered the head from %d to %d", pieceSize/testMiB, baseline, withFloor)
		}
		if got, floorPieces := withFloor, pieceFloorFor(p, pieceSize)/pieceSize; floorPieces > 1 && got == baseline {
			t.Logf("piece %d MiB: floor raised to %d pieces", pieceSize/testMiB, floorPieces)
		}
	}
}

// A 16 MiB piece at remux bitrate is 2 seconds — a cliff the byte-threshold
// version of this fix would have missed entirely, because 16 MiB is under the
// 32 MiB it tested against. The trigger has to be time, not size.
func TestModeratePiecesAtHighBitrateAlsoGetTheFloor(t *testing.T) {
	p := finalReckoningProfile()
	const pieceSize = 16 * testMiB
	if runway := float64(pieceSize) / p.BytesPerSecond(); runway >= lowRunwaySeconds {
		t.Fatalf("test premise broken: one piece is %.1fs", runway)
	}
	got := HeadBytesForPieceTracking(p, 400*testMiB, pieceSize)
	if got < 2*pieceSize {
		t.Errorf("head = %d (%d pieces), want at least 2", got, got/pieceSize)
	}
}

// Without a runtime there is no way to turn a piece into seconds, and guessing
// one would be a fabricated measurement. One piece stands.
func TestNoProfileMeansNoFloorChange(t *testing.T) {
	const pieceSize = 64 * testMiB
	p := PlaybackProfile{FileBytes: 80 * 1024 * testMiB} // no runtime
	if got := pieceFloorFor(p, pieceSize); got != pieceSize {
		t.Errorf("floor = %d, want one piece when the runtime is unknown", got)
	}
}

// The floor must never turn into most of the file: on something small enough
// that two pieces exceed the streamable fraction, waiting for the second costs
// more than the cliff it prevents.
func TestFloorNeverSwallowsASmallFile(t *testing.T) {
	const pieceSize = 64 * testMiB
	// A 30-second clip at remux bitrate: two pieces would be 64% of it.
	p := PlaybackProfile{FileBytes: 200 * testMiB, RuntimeSeconds: 25}
	if runway := float64(pieceSize) / p.BytesPerSecond(); runway >= lowRunwaySeconds {
		t.Fatalf("test premise broken: one piece is %.1fs", runway)
	}
	if got := pieceFloorFor(p, pieceSize); got != pieceSize {
		t.Errorf("floor = %d bytes of a %d byte file, want one piece", got, p.FileBytes)
	}
}

// The alignment site in the prepare loop applies the same floor, since
// reviseHead can only lower a requirement and never raise one.
func TestAlignmentAppliesTheFloor(t *testing.T) {
	p := finalReckoningProfile()
	const pieceSize = 64 * testMiB
	if got := AlignHeadToPiecesFor(1*testMiB, pieceSize, p); got != 2*pieceSize {
		t.Errorf("aligned head = %d (%d pieces), want 2", got, got/pieceSize)
	}
	// And leaves an already-deep head alone.
	if got := AlignHeadToPiecesFor(10*pieceSize, pieceSize, p); got != 10*pieceSize {
		t.Errorf("aligned head = %d, want the requested 10 pieces untouched", got)
	}
}
