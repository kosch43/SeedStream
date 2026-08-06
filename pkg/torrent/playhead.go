package torrent

import (
	"sync/atomic"
)

// Playhead is where the viewer is in a stream, and how much downloaded data
// lies between them and the edge of the file.
//
// It exists because "how far along is the download" and "will this keep
// playing" are different questions. A torrent can be downloading briskly while
// the run of data in front of the playhead does not grow at all — that is what
// a stall looks like from the inside, several seconds before the player shows
// it. Knowing the position turns that from something discovered on the viewer's
// screen into something visible while there is still time to act.
//
// Safe for concurrent use: the reader writes, the dashboard reads.
type Playhead struct {
	fileSize   int64
	runtimeSec int64

	pos      atomic.Int64 // last byte handed to the player
	furthest atomic.Int64 // furthest byte reached, ignoring backward seeks
	frontier atomic.Int64 // end of the contiguous downloaded run ahead of pos
	seeks    atomic.Int64
	started  atomic.Bool
}

// NewPlayhead returns a tracker for a file of the given size. runtimeSec is the
// title's runtime in seconds, or 0 when it is unknown — without it a byte
// offset cannot be placed on the timeline, and the position is reported in
// bytes alone rather than guessed at.
func NewPlayhead(fileSize, runtimeSec int64) *Playhead {
	if fileSize <= 0 {
		return nil
	}
	return &Playhead{fileSize: fileSize, runtimeSec: runtimeSec}
}

// observeRead records bytes handed to the player.
func (p *Playhead) observeRead(pos int64) {
	if p == nil {
		return
	}
	p.started.Store(true)
	p.pos.Store(pos)
	for {
		cur := p.furthest.Load()
		if pos <= cur || p.furthest.CompareAndSwap(cur, pos) {
			return
		}
	}
}

// observeSeek records a jump to a new position. Players seek constantly — every
// range request after a reconnect is one — so this counts them rather than
// treating each as an event.
func (p *Playhead) observeSeek(pos int64) {
	if p == nil {
		return
	}
	if p.started.Load() {
		p.seeks.Add(1)
	}
	p.pos.Store(pos)
}

// noteFrontier records where the contiguous downloaded run currently ends.
func (p *Playhead) noteFrontier(end int64) {
	if p == nil {
		return
	}
	p.frontier.Store(end)
}

// PlaybackPosition is a snapshot of where the viewer is and how much runway
// they have. Byte offsets are exact; everything expressed in seconds is derived
// from them.
type PlaybackPosition struct {
	ByteOffset int64   `json:"byte_offset"`
	FileSize   int64   `json:"file_size"`
	Percent    float64 `json:"percent"`
	// PositionSeconds and RunwaySeconds are 0 when the runtime is unknown.
	//
	// They are derived by treating the file as a constant bitrate, which is what
	// every player does for progressive HTTP content with no index available.
	// For variable-bitrate video the figure drifts against the true timestamp —
	// a quiet scene occupies fewer bytes per second than a busy one — so it is a
	// good indication of position, not a frame-accurate one.
	PositionSeconds int64 `json:"position_seconds"`
	RuntimeSeconds  int64 `json:"runtime_seconds"`
	// RunwayBytes is the downloaded data between the playhead and the first
	// missing byte. RunwaySeconds is that expressed as playing time — how long
	// the stream can continue if the download stopped dead right now.
	RunwayBytes   int64 `json:"runway_bytes"`
	RunwaySeconds int64 `json:"runway_seconds"`
	Seeks         int64 `json:"seeks"`
}

// Position returns the current snapshot.
func (p *Playhead) Position() PlaybackPosition {
	if p == nil {
		return PlaybackPosition{}
	}
	pos := p.pos.Load()
	out := PlaybackPosition{
		ByteOffset:     pos,
		FileSize:       p.fileSize,
		RuntimeSeconds: p.runtimeSec,
		Seeks:          p.seeks.Load(),
	}
	if p.fileSize > 0 {
		out.Percent = float64(pos) / float64(p.fileSize) * 100
	}
	if front := p.frontier.Load(); front > pos {
		out.RunwayBytes = front - pos
	}
	if p.runtimeSec > 0 && p.fileSize > 0 {
		bytesPerSec := float64(p.fileSize) / float64(p.runtimeSec)
		out.PositionSeconds = int64(float64(pos) / bytesPerSec)
		out.RunwaySeconds = int64(float64(out.RunwayBytes) / bytesPerSec)
	}
	return out
}
