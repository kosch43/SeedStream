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
	if want > MaxHeadBytes {
		want = MaxHeadBytes
	}
	// A tenth of the file is minutes of video at any bitrate, so it is always
	// enough to start on, and it keeps a bad runtime estimate from becoming a
	// long wait.
	if ceiling := int64(float64(fileBytes) * StreamableHeadFraction); ceiling > 0 && want > ceiling && ceiling >= MinHeadBytes {
		want = ceiling
	}
	if want < MinHeadBytes {
		want = MinHeadBytes
	}
	if want > fileBytes {
		want = fileBytes
	}
	return want
}
