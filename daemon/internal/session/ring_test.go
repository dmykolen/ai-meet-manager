package session

import (
	"testing"
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/source"
)

func frame(v int16) []int16 {
	f := make([]int16, 2*source.FrameSize)
	for i := range f {
		f[i] = v
	}
	return f
}

// values reads the first sample of each replayed frame, which is enough to say
// which frames came back and in what order.
func values(frames [][]int16) []int16 {
	out := make([]int16, len(frames))
	for i, f := range frames {
		out[i] = f[0]
	}
	return out
}

func TestReplayReturnsTheMostRecentAudioOldestFirst(t *testing.T) {
	r := NewRing(10 * frameDuration)
	for i := range int16(10) {
		r.Add(frame(i))
	}
	got := values(r.Replay(3 * frameDuration))
	if len(got) != 3 || got[0] != 7 || got[1] != 8 || got[2] != 9 {
		t.Fatalf("replayed %v, want the last three in order", got)
	}
}

func TestTheRingOverwritesTheOldestAudio(t *testing.T) {
	r := NewRing(4 * frameDuration)
	for i := range int16(10) { // two and a half times round
		r.Add(frame(i))
	}
	got := values(r.Replay(time.Hour)) // more than it holds
	if len(got) != 4 || got[0] != 6 || got[3] != 9 {
		t.Fatalf("replayed %v, want frames 6..9", got)
	}
}

func TestAPartlyFilledRingReplaysOnlyWhatItHas(t *testing.T) {
	// The case that matters at startup, when a meeting begins before the ring
	// has had time to fill.
	r := NewRing(time.Minute)
	for i := range int16(3) {
		r.Add(frame(i))
	}
	got := values(r.Replay(time.Minute))
	if len(got) != 3 || got[0] != 0 || got[2] != 2 {
		t.Fatalf("replayed %v, want the three frames it has", got)
	}
	if held := r.Held(); held != 3*frameDuration {
		t.Fatalf("Held()=%v, want %v", held, 3*frameDuration)
	}
}

func TestTheRingIsSizedForTheAudioItIsAskedToHold(t *testing.T) {
	r := NewRing(10 * time.Minute)
	for range 20000 {
		r.Add(frame(1))
	}
	held := r.Held()
	if held < 9*time.Minute+50*time.Second || held > 10*time.Minute {
		t.Fatalf("Held()=%v, want about ten minutes", held)
	}
}

func TestReplayingNothingIsNotAnError(t *testing.T) {
	if got := NewRing(time.Minute).Replay(time.Minute); len(got) != 0 {
		t.Fatalf("an empty ring replayed %d frames", len(got))
	}
}
