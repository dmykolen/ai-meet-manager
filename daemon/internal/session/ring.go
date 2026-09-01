package session

import (
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/source"
)

// Ring keeps the last few minutes of audio in memory so that a recording can
// begin in the past.
//
// This is the feature the whole daemon exists for: by the time anything can
// tell that a meeting is happening, the meeting has been going for a while.
// Detection fires, the ring is replayed, and the first minutes are there.
//
// Storage is flat and preallocated — ten minutes of stereo int16 at 16 kHz is
// 38 MB, and churning that through the garbage collector every ten minutes for
// the life of a background daemon would be a strange thing to do.
type Ring struct {
	frames  [][]int16
	next    int
	written int
}

// NewRing sizes a buffer to hold d of audio.
func NewRing(d time.Duration) *Ring {
	count := int(d / frameDuration)
	if count < 1 {
		count = 1
	}
	frames := make([][]int16, count)
	for i := range frames {
		frames[i] = make([]int16, 2*source.FrameSize)
	}
	return &Ring{frames: frames}
}

const frameDuration = time.Duration(source.FrameSize) * time.Second / source.SampleRate

// Add copies one stereo frame in, overwriting the oldest once full.
func (r *Ring) Add(frame []int16) {
	copy(r.frames[r.next], frame)
	r.next = (r.next + 1) % len(r.frames)
	r.written++
}

// Held is how much audio the ring currently has.
func (r *Ring) Held() time.Duration {
	return time.Duration(min(r.written, len(r.frames))) * frameDuration
}

// Replay yields the most recent d of audio, oldest first. Asking for more than
// the ring holds gives everything it has.
//
// The frames are the ring's own storage and are overwritten as capture
// continues, so a caller must consume each one before taking the next. In
// practice that is a write to disk, which is what this is for.
func (r *Ring) Replay(d time.Duration) [][]int16 {
	want := min(int(d/frameDuration), min(r.written, len(r.frames)))
	out := make([][]int16, 0, want)
	// r.next is the oldest slot once the ring has wrapped, and the write head
	// before that; counting back from it covers both cases.
	start := ((r.next-want)%len(r.frames) + len(r.frames)) % len(r.frames)
	for i := range want {
		out = append(out, r.frames[(start+i)%len(r.frames)])
	}
	return out
}
