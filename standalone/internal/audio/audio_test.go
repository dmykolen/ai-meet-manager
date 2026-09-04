package audio

import (
	"context"
	"testing"
	"time"
)

// fake stands in for a capture device. The mixer under test is the real one —
// stubbing the mixer would hide exactly the bug worth catching, since its whole
// job is reconciling two clocks that never agree.
type fake struct{ out chan []int16 }

func newFake() *fake                    { return &fake{out: make(chan []int16, 256)} }
func (f *fake) Samples() <-chan []int16 { return f.out }
func (f *fake) Close() error            { return nil }

// send pushes n samples all of value v, in FrameSize blocks.
func (f *fake) send(v int16, n int) {
	for sent := 0; sent < n; sent += FrameSize {
		block := make([]int16, min(FrameSize, n-sent))
		for i := range block {
			block[i] = v
		}
		f.out <- block
	}
}

// collect reads frames until the stream stops producing them.
func collect(t *testing.T, s *Stream, want int) [][]int16 {
	t.Helper()
	var got [][]int16
	deadline := time.After(2 * time.Second)
	for len(got) < want {
		select {
		case frame, ok := <-s.Frames():
			if !ok {
				return got
			}
			got = append(got, frame)
		case <-deadline:
			t.Fatalf("timed out after %d frames, wanted %d", len(got), want)
		}
	}
	return got
}

func TestChannelsLandOnTheirOwnSides(t *testing.T) {
	mic, sys := newFake(), newFake()
	stream := newStream(context.Background(), mic, sys)
	defer stream.Close()

	mic.send(100, 2*FrameSize)
	sys.send(-100, 2*FrameSize)

	for _, frame := range collect(t, stream, 2) {
		for i := 0; i < len(frame); i += 2 {
			if frame[i] != 100 {
				t.Fatalf("left carried %d, want the microphone's 100", frame[i])
			}
			if frame[i+1] != -100 {
				t.Fatalf("right carried %d, want the system audio's -100", frame[i+1])
			}
		}
	}
}

func TestASilentSystemChannelDoesNotStallTheMicrophone(t *testing.T) {
	// The case that matters on a machine with no tap, and during the ~1.5 s the
	// macOS tap needs to warm up: the microphone must keep flowing regardless.
	mic, sys := newFake(), newFake()
	stream := newStream(context.Background(), mic, sys)
	defer stream.Close()

	mic.send(7, 3*FrameSize) // and nothing at all on sys

	for _, frame := range collect(t, stream, 3) {
		for i := 0; i < len(frame); i += 2 {
			if frame[i] != 7 {
				t.Fatalf("left carried %d, want 7", frame[i])
			}
			if frame[i+1] != 0 {
				t.Fatalf("right carried %d, want silence", frame[i+1])
			}
		}
	}
}

func TestSystemAudioRunningAheadIsTrimmedNotAccumulated(t *testing.T) {
	// Two clocks drift. If the faster one were simply queued, the right channel
	// would slide further behind the left for as long as the meeting lasts.
	mic, sys := newFake(), newFake()
	stream := newStream(context.Background(), mic, sys)
	defer stream.Close()

	sys.send(-1, 4*slack) // far more than the mixer will hold
	mic.send(1, 4*FrameSize)

	frames := collect(t, stream, 4)
	if len(frames) < 4 {
		t.Fatalf("got %d frames, want 4", len(frames))
	}
	// What matters is that the right channel is still live after the trim, not
	// which samples survived: dropping the oldest is the point. Only the first
	// frame is checked, because the bound is deliberately small — a backlog
	// that outlives it is the audible echo this trim exists to prevent.
	for i := 1; i < len(frames[0]); i += 2 {
		if frames[0][i] != -1 {
			t.Fatalf("right went dead immediately after the trim: %d", frames[0][i])
		}
	}
}

func TestTheSystemChannelIsNeverHeldLongEnoughToEcho(t *testing.T) {
	// The bug this bound exists for: the backlog used to be half a second and
	// was only ever trimmed at the bound, so it sat just under it for the whole
	// meeting. Anybody without headphones heard every remote sentence twice.
	//
	// Thirty to forty milliseconds is where a delayed copy stops being heard as
	// part of the same sound, and one frame is the floor below which the system
	// channel would starve. The bound has to sit between them.
	if held := float64(slack) / SampleRate; held > 0.15 {
		t.Fatalf("the system channel may lag by %.0f ms, which is heard as an echo", held*1000)
	}
	if slack < 3*FrameSize {
		t.Fatalf("slack is %d samples, under three frames; the system channel will starve", slack)
	}
}

func TestStreamWithoutSystemAudioSaysSo(t *testing.T) {
	mic := newFake()
	stream := newStream(context.Background(), mic, nil)
	defer stream.Close()

	if stream.SystemAudio {
		t.Fatal("SystemAudio must be false when there is no device; every meeting would look like a monologue")
	}
	mic.send(5, FrameSize)
	if frame := collect(t, stream, 1)[0]; frame[1] != 0 {
		t.Fatalf("right carried %d with no device, want silence", frame[1])
	}
}

func TestClosingStopsTheMixer(t *testing.T) {
	mic := newFake()
	stream := newStream(context.Background(), mic, nil)
	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case _, ok := <-stream.Frames():
		if ok {
			t.Fatal("frames still arriving after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("mixer did not stop")
	}
}
