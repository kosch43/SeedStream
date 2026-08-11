package torrent

import "math"

// How much of a file's start must be on disk before playback begins.
//
// The question is not "how big is this file" but "will the download stay ahead
// of the player". Those give wildly different answers: a 99 Mbps remux arriving
// at 557 MB/s needs a moment's cushion, while the same remux trickling in at
// 2 MB/s needs most of the file. Sizing from bitrate alone cannot tell them
// apart, so it has to be wrong for one of them — and it was wrong for the fast
// case, holding back a stream that could have started immediately.
const (
	// jitterSeconds is the cushion when the download comfortably outruns
	// playback. It absorbs variance in peer throughput, not sustained
	// starvation — with a real margin the file finishes long before the player
	// reaches the parts still arriving.
	jitterSeconds = 3

	// rateDipFactor derates the observed download rate before the arithmetic.
	//
	// Without it the model has a cliff: at 0.99x playback speed it asks for
	// 300 MB and at 1.01x for nothing, so a two-percent wobble in a rate
	// sampled over a second and a half swings the requirement a hundredfold.
	// Assuming the rate may halve moves the cliff out to twice playback speed,
	// where a dip has somewhere to fall before it starves the player.
	rateDipFactor = 2

	// MinHeadBytes is the smallest head worth starting on. A single 4 MiB piece
	// is enough to pass a piece check but not enough runway for a player to get
	// through its initial probes before the next piece arrives. Four pieces at
	// the common 4 MiB qBittorrent piece size gives the reader room to start
	// without immediately catching the download frontier.
	MinHeadBytes int64 = 16 * 1024 * 1024

	// MaxHeadBytes bounds startup on an extreme bitrate, and keeps the prepare
	// budget from being consumed entirely by buffering.
	MaxHeadBytes int64 = 384 * 1024 * 1024
)

// PlaybackProfile is what the head calculation needs to know about the content:
// how many bytes it is and how long it plays for. Together they give the rate
// at which playback will consume it.
type PlaybackProfile struct {
	FileBytes      int64
	RuntimeSeconds int64
}

// Valid reports whether there is enough here to compute anything.
func (p PlaybackProfile) Valid() bool {
	return p.FileBytes > 0 && p.RuntimeSeconds > 0
}

// BytesPerSecond is the rate playback consumes the file at.
func (p PlaybackProfile) BytesPerSecond() float64 {
	if !p.Valid() {
		return 0
	}
	return float64(p.FileBytes) / float64(p.RuntimeSeconds)
}

// HeadBytesFor returns how much of the file's start must be continuously on
// disk before playback begins.
//
// dlBytesPerSec is the torrent's observed download rate; pass 0 when it is not
// known yet, which is the case before the torrent has been added and found
// peers. An unknown rate falls back to a table indexed on bitrate — the same
// figures used before rate was consulted at all — because guessing a rate would
// be worse than using the one thing that is actually known.
//
// The result is always bounded: never below MinHeadBytes, never above
// MaxHeadBytes, and normally no more than a tenth of the file. For a tiny file
// where a tenth is below MinHeadBytes, the minimum runway wins and the whole
// file may be required.
func HeadBytesFor(p PlaybackProfile, dlBytesPerSec int64) int64 {
	if !p.Valid() {
		return 0
	}
	playback := p.BytesPerSecond()
	seconds := headSecondsFor(p, dlBytesPerSec)
	return clampHead(int64(math.Ceil(float64(seconds)*playback)), p.FileBytes)
}

// headSecondsFor returns how many seconds of video to hold before starting.
func headSecondsFor(p PlaybackProfile, dlBytesPerSec int64) float64 {
	playback := p.BytesPerSecond()
	if dlBytesPerSec <= 0 || playback <= 0 {
		return float64(legacyTierSeconds(bitrateMbps(p)))
	}

	// Derated so a dip has room to fall before it starves the player.
	effective := float64(dlBytesPerSec) / rateDipFactor
	if effective >= playback {
		// The download is ahead and will stay ahead through any ordinary
		// wobble. Only jitter needs covering: at this margin the file finishes
		// well before the player reaches the parts still on their way.
		return jitterSeconds
	}

	// The download cannot keep up. The head has to carry the difference for as
	// long as the shortfall lasts, which ends when the file finishes arriving —
	// after that there is nothing left to wait for. Bounded by the runtime,
	// since playback ends there whatever the download is doing.
	downloadSeconds := float64(p.FileBytes) / float64(dlBytesPerSec)
	shortfallFor := math.Min(downloadSeconds, float64(p.RuntimeSeconds))
	deficit := 1 - effective/playback
	seconds := shortfallFor * deficit
	if seconds < jitterSeconds {
		seconds = jitterSeconds
	}
	return seconds
}

// bitrateMbps is the content's bitrate, used only for the unknown-rate table.
func bitrateMbps(p PlaybackProfile) float64 {
	return p.BytesPerSecond() * 8 / 1e6
}

// legacyTierSeconds is the bitrate table used when the download rate is not yet
// known. It encodes the same assumption the tiers always did — that a
// higher-bitrate release is closer to outrunning its download — which is a
// reasonable prior, and no more than that. Once a real rate is available the
// arithmetic above replaces it.
func legacyTierSeconds(bitrate float64) int64 {
	switch {
	case bitrate < 6: // 1080p x265 / WEB-DL
		return 8
	case bitrate < 16: // 1080p Blu-ray, high-bitrate WEB-DL
		return 15
	case bitrate < 40: // 4K WEB-DL, 1080p remux
		return 30
	default: // 4K remux
		return 45
	}
}

// clampHead applies the bounds that hold whatever the arithmetic produced.
func clampHead(want, fileBytes int64) int64 {
	return clampHeadTo(want, fileBytes, MinHeadBytes)
}

// clampHeadTo is clampHead with an explicit floor. A caller that can prove a
// contiguous run through the piece bitmap may lower the floor to one piece,
// where the byte-default MinHeadBytes (four 4 MiB pieces) is unnecessary.
func clampHeadTo(want, fileBytes, minHead int64) int64 {
	if want > MaxHeadBytes {
		want = MaxHeadBytes
	}
	// A tenth of the file is minutes of video at any bitrate, so it is always
	// enough to start on, and it keeps a bad runtime estimate from becoming a
	// long wait.
	if ceiling := int64(float64(fileBytes) * StreamableHeadFraction); ceiling > 0 && want > ceiling && ceiling >= minHead {
		want = ceiling
	}
	if want < minHead {
		want = minHead
	}
	if want > fileBytes {
		want = fileBytes
	}
	return want
}

// AlignHeadToPieces rounds a head requirement up to whole pieces and floors it
// at one piece.
//
// The head is checked against the piece bitmap, so a byte target is really a
// piece target: 16 MiB means four consecutive 4 MiB pieces but only half of a
// 32 MiB piece. Sizing in bytes therefore asks small-piece torrents for four
// times the consecutive run, which is exactly where out-of-order arrival is
// most likely to leave a hole.
func AlignHeadToPieces(head, pieceSize int64) int64 {
	if pieceSize <= 0 {
		return head
	}
	if head < pieceSize {
		return pieceSize
	}
	return ((head + pieceSize - 1) / pieceSize) * pieceSize
}

// AlignHeadToPiecesFor is AlignHeadToPieces with the anti-cliff floor applied,
// for callers that know what the content plays at.
func AlignHeadToPiecesFor(head, pieceSize int64, p PlaybackProfile) int64 {
	aligned := AlignHeadToPieces(head, pieceSize)
	if floor := pieceFloorFor(p, pieceSize); aligned < floor {
		aligned = floor
	}
	return aligned
}

// pieceFloorFor is the smallest head worth starting on when the head is counted
// in pieces: one piece, or two when one piece is less playback time than the
// reader itself treats as a likely stall.
//
// One piece is normally plenty of runway — a 4 MiB piece is seconds of video,
// and the whole point of the piece-tracking floor is not to demand four of them
// when the bitmap can prove one is contiguous. But piece size does not scale
// with anything the player cares about. An 80 GB remux published with 64 MiB
// pieces is 7.9 seconds per piece at 67.8 Mbps, and SeedStream would start
// playback on exactly one of them — a cliff, with nothing cued behind it, which
// the reader then immediately reports as "the viewer is catching up with the
// download" because 7.9 is below its own lowRunwaySeconds. Two parts of the
// same system disagreeing about whether that is enough is the bug.
//
// Two pieces, not more: the guarantee wanted here is that the pipeline is never
// empty — while the player drains the first piece, the second is already in
// hand and the third has a full piece-time to arrive. Making it deeper on a
// prediction is the wrong instrument; the fragmented-head escalation in the
// prepare loop already does that, and it acts on out-of-order arrival actually
// observed rather than guessed at.
//
// Deliberately silent for small pieces. A 4 MiB piece at this bitrate is half a
// second, so the byte-sized head is already many pieces and this floor never
// binds — which is what keeps the fast start on ordinary releases untouched.
func pieceFloorFor(p PlaybackProfile, pieceSize int64) int64 {
	if pieceSize <= 0 {
		return pieceSize
	}
	playback := p.BytesPerSecond()
	if playback <= 0 {
		// No runtime, so no way to turn a piece into seconds. One piece stands:
		// inventing a bitrate to justify doubling the wait would be a guess
		// dressed as a measurement.
		return pieceSize
	}
	if float64(pieceSize)/playback >= lowRunwaySeconds {
		return pieceSize // one piece is already more runway than a stall warning
	}
	two := pieceSize * 2
	// Never let the anti-cliff floor become most of the file. On a file small
	// enough that two pieces exceed the streamable fraction, waiting for the
	// second costs more than the cliff it prevents.
	if ceiling := int64(float64(p.FileBytes) * StreamableHeadFraction); ceiling > 0 && two > ceiling {
		return pieceSize
	}
	return two
}

// HeadBytesForPieceTracking is HeadBytesFor with the floor lowered to one piece,
// for callers that can prove a contiguous run via the piece bitmap rather than
// assume one from byte progress. A single piece at the playhead is enough to
// pass the head check there, so the four-piece MinHeadBytes floor is not needed.
//
// Returns HeadBytesFor's answer when pieceSize is not positive.
func HeadBytesForPieceTracking(p PlaybackProfile, dlBytesPerSec, pieceSize int64) int64 {
	if !p.Valid() || pieceSize <= 0 {
		return HeadBytesFor(p, dlBytesPerSec)
	}
	playback := p.BytesPerSecond()
	seconds := headSecondsFor(p, dlBytesPerSec)
	want := int64(math.Ceil(float64(seconds) * playback))
	return clampHeadTo(want, p.FileBytes, pieceFloorFor(p, pieceSize))
}
