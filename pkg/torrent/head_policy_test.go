package torrent

import (
	"testing"
)

// remuxProfile is the release from the field measurement: a 4K remux at
// 99 Mbps, which is 12.4 MB/s of playback.
func remuxProfile() PlaybackProfile {
	// 12.4 MB/s * 7200s = 89.3 GB. Sized so BytesPerSecond lands on 12.4 MB/s.
	return PlaybackProfile{FileBytes: 12_400_000 * 7200, RuntimeSeconds: 7200}
}

// TestFastDownloadNeedsOnlyJitter is the fix. Playback consumes 12.4 MB/s and
// the seedbox delivers 557 — forty-five times faster — so the file is complete
// long before the player reaches the parts still arriving. Demanding 45 seconds
// of video there is 402 MB of waiting for a stream that could start on 40.
func TestFastDownloadNeedsOnlyJitter(t *testing.T) {
	p := remuxProfile()
	got := HeadBytesFor(p, 557_000_000)

	// jitterSeconds of video, give or take rounding.
	want := int64(jitterSeconds * p.BytesPerSecond())
	if got > want+(1<<20) || got < MinHeadBytes {
		t.Fatalf("a 45x-faster download needs only jitter: got %d, want about %d", got, want)
	}
	// And it must be dramatically smaller than the rate-blind answer.
	blind := HeadBytesFor(p, 0)
	if got*4 > blind {
		t.Fatalf("rate-aware head %d is not meaningfully smaller than the rate-blind %d", got, blind)
	}
}

// TestSlowDownloadNeedsMoreHead is the same formula working in the other
// direction, which is what makes it self-correcting: a swarm that cannot keep up
// automatically demands a larger head instead of starting a stream that will
// starve. The fixed table could not tell the two cases apart.
func TestSlowDownloadNeedsMoreHead(t *testing.T) {
	p := remuxProfile()
	fast := HeadBytesFor(p, 557_000_000)
	slow := HeadBytesFor(p, 2_000_000) // 2 MB/s against 12.4 MB/s of playback

	if slow <= fast {
		t.Fatalf("a download slower than playback must ask for more head, got %d against %d", slow, fast)
	}
	if slow > MaxHeadBytes {
		t.Fatalf("head %d exceeds the cap %d", slow, MaxHeadBytes)
	}
}

// TestNoCliffAroundPlaybackSpeed guards the reason the observed rate is derated.
//
// The bare model is discontinuous: at 0.99x playback speed it wants 300 MB and
// at 1.01x it wants nothing. A rate sampled over a second and a half wobbles by
// more than that, so the requirement would swing a hundredfold between polls on
// a download that had not changed. Nothing near playback speed may collapse to
// the jitter floor.
func TestNoCliffAroundPlaybackSpeed(t *testing.T) {
	p := remuxProfile()
	playback := int64(p.BytesPerSecond())
	jitter := int64(jitterSeconds * p.BytesPerSecond())

	for _, mult := range []float64{0.99, 1.0, 1.01, 1.5, 1.9} {
		got := HeadBytesFor(p, int64(float64(playback)*mult))
		if got <= jitter*2 {
			t.Errorf("at %.2fx playback speed the head collapsed to %d — too close to the jitter floor %d",
				mult, got, jitter)
		}
	}
	// Past twice playback speed there is real margin, and jitter is enough.
	if got := HeadBytesFor(p, playback*2); got > jitter+(1<<20) {
		t.Errorf("at twice playback speed jitter should suffice, got %d against %d", got, jitter)
	}
}

// TestUnknownRateKeepsTheBitrateTable: before the torrent has peers there is no
// rate to read. Guessing one would be worse than using the bitrate prior, which
// is the only thing actually known at that point.
func TestUnknownRateKeepsTheBitrateTable(t *testing.T) {
	p := remuxProfile()
	got := HeadBytesFor(p, 0)
	want := int64(legacyTierSeconds(bitrateMbps(p))) * int64(p.BytesPerSecond())
	if want > MaxHeadBytes {
		want = MaxHeadBytes
	}
	if got != want {
		t.Fatalf("unknown rate must use the table: got %d, want %d", got, want)
	}
}

// TestHeadBoundsAlwaysHold: whatever the arithmetic produces, the rails stay up.
// These each fixed a verified failure and the rate-awareness must not weaken
// any of them.
func TestHeadBoundsAlwaysHold(t *testing.T) {
	cases := []struct {
		name string
		p    PlaybackProfile
		rate int64
	}{
		{"absurdly fast", remuxProfile(), 1 << 40},
		{"absurdly slow", remuxProfile(), 1},
		{"tiny file", PlaybackProfile{FileBytes: 50_000_000, RuntimeSeconds: 1200}, 1 << 30},
		{"tiny file, crawling", PlaybackProfile{FileBytes: 50_000_000, RuntimeSeconds: 1200}, 1000},
		{"unknown rate", remuxProfile(), 0},
	}
	for _, tc := range cases {
		got := HeadBytesFor(tc.p, tc.rate)
		if got < MinHeadBytes && got != tc.p.FileBytes {
			t.Errorf("%s: head %d is below the floor %d", tc.name, got, MinHeadBytes)
		}
		if got > MaxHeadBytes {
			t.Errorf("%s: head %d exceeds the cap %d", tc.name, got, MaxHeadBytes)
		}
		if ceiling := int64(float64(tc.p.FileBytes) * StreamableHeadFraction); ceiling >= MinHeadBytes && got > ceiling {
			t.Errorf("%s: head %d exceeds a tenth of the file (%d)", tc.name, got, ceiling)
		}
		if got > tc.p.FileBytes {
			t.Errorf("%s: head %d exceeds the file itself (%d)", tc.name, got, tc.p.FileBytes)
		}
	}
}

// TestInvalidProfileComputesNothing: without a size or a runtime there is no
// playback rate, so no arithmetic is possible and the caller keeps its default.
func TestInvalidProfileComputesNothing(t *testing.T) {
	for _, p := range []PlaybackProfile{
		{},
		{FileBytes: 1 << 30},
		{RuntimeSeconds: 7200},
	} {
		if got := HeadBytesFor(p, 1<<20); got != 0 {
			t.Errorf("an incomplete profile must compute nothing, got %d for %+v", got, p)
		}
	}
}
