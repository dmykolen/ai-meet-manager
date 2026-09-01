// Package source captures the microphone and the system audio as one stereo
// stream: left is the microphone, right is whatever the machine is playing.
//
// Keeping them apart is what removes the need for echo cancellation — the other
// participants arrive clean from the system tap rather than through the room —
// and it is also the whole meeting detector. Speech on the right channel means
// somebody is talking to you.
//
// Everything platform-specific lives in this package, behind Device. Nothing
// above it branches on GOOS.
package source

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	// SampleRate is fixed at what Whisper and pyannote both want, so nothing
	// downstream ever resamples.
	SampleRate = 16000

	// FrameSize is Silero's window: 512 samples, 32 ms at 16 kHz. Using it as
	// the unit everywhere means the VAD never has to buffer or split.
	FrameSize = 512

	// slack is how far the system stream may run ahead of the microphone before
	// samples are dropped. The two are separate clocks, so one always drifts;
	// half a second absorbs the jitter, and anything beyond that is a stall
	// worth reporting rather than hiding.
	slack = SampleRate / 2
)

// A Device is one 16 kHz mono int16 source. Samples arrive on Samples until the
// device is closed, at which point the channel is closed too.
type Device interface {
	Samples() <-chan []int16
	Close() error
}

// Stream is the microphone and the system audio interleaved into stereo frames
// of FrameSize samples per channel.
type Stream struct {
	frames chan []int16

	// SystemAudio reports whether the right channel has a real device behind
	// it. When false the right channel is silence and only notes can be
	// detected — every meeting would look like a monologue.
	SystemAudio bool

	closeOnce sync.Once
	devices   []Device
	done      chan struct{}
	wg        sync.WaitGroup
}

// Frames yields interleaved stereo frames, 2*FrameSize samples each.
func (s *Stream) Frames() <-chan []int16 { return s.frames }

// Close stops both devices and drains the mixer.
func (s *Stream) Close() error {
	err := error(nil)
	s.closeOnce.Do(func() {
		close(s.done)
		for _, d := range s.devices {
			err = errors.Join(err, d.Close())
		}
		s.wg.Wait()
	})
	return err
}

// Waiting is when Open stops assuming the devices are merely slow and starts
// saying what it is probably waiting for.
const Waiting = 4 * time.Second

// Open starts capture. A microphone is mandatory; the system tap is not, because
// a machine without one still records useful notes and saying so is better than
// refusing to run.
func Open(ctx context.Context) (*Stream, error) {
	// Opening the microphone blocks until macOS has an answer about the
	// permission, and when there is a dialog on screen that answer is a person.
	// There is deliberately no timeout: the wait is legitimate, and the menu bar
	// item is already up to show it. An earlier version timed out and retried,
	// which was worse — every abandoned attempt was still blocked inside
	// CoreAudio, and when the permission finally arrived three microphones
	// opened at once and two of them had nobody reading them.
	settled := make(chan struct{})
	defer close(settled)
	go func() {
		select {
		case <-settled:
		case <-time.After(Waiting):
			slog.Warn("still opening the audio devices — macOS is probably asking " +
				"for permission. Look for a dialog on screen, or open System Settings > " +
				"Privacy & Security and enable Microphone and System Audio Recording for mtd")
		}
	}()

	mic, err := openMicrophone()
	if err != nil {
		return nil, err
	}

	sys, err := openSystemAudio()
	if err != nil {
		slog.Warn("no system audio; only your own voice will be recorded", "err", err)
		sys = nil
	}

	return newStream(ctx, mic, sys), nil
}

// newStream wires two devices into one stereo stream. Open is the real entry
// point; this is the seam that lets the mixer be driven by fake devices, since
// its whole job is reconciling two clocks that never quite agree.
func newStream(ctx context.Context, mic, sys Device) *Stream {
	s := &Stream{
		frames:      make(chan []int16, 64),
		SystemAudio: sys != nil,
		devices:     []Device{mic},
		done:        make(chan struct{}),
	}
	if sys != nil {
		s.devices = append(s.devices, sys)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(s.frames)
		s.mix(ctx, mic, sys)
	}()
	return s
}

// mix interleaves the two devices, using the microphone as the clock.
//
// The microphone is driven by the audio hardware at exactly the rate we asked
// for, which makes it the honest clock; the system stream is buffered against
// it. Sample-accurate alignment is not needed — the two channels are downmixed
// by the server anyway, and a few milliseconds of skew changes nothing about
// which channel had speech.
func (s *Stream) mix(ctx context.Context, mic, sys Device) {
	var (
		left    []int16
		right   []int16
		sysOpen = sys != nil
		sysCh   <-chan []int16
		dropped int
	)
	if sysOpen {
		sysCh = sys.Samples()
	}

	for {
		// Take everything the system stream has without waiting for it: it must
		// never hold up the microphone, which is what keeps time.
		for drain := true; drain && sysOpen; {
			select {
			case block, ok := <-sysCh:
				if !ok {
					sysOpen, sysCh = false, nil
					slog.Warn("system audio stopped; right channel goes silent")
					continue
				}
				right = append(right, block...)
			default:
				drain = false
			}
		}
		if len(right) > slack {
			dropped += len(right) - slack
			right = right[len(right)-slack:]
			slog.Warn("system audio ran ahead; dropped samples", "total", dropped)
		}

		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case block, ok := <-mic.Samples():
			if !ok {
				return
			}
			left = append(left, block...)
		}

		for len(left) >= FrameSize {
			frame := make([]int16, 2*FrameSize)
			for i := range FrameSize {
				frame[2*i] = left[i]
				if i < len(right) {
					frame[2*i+1] = right[i]
				}
			}
			left = left[FrameSize:]
			// A short right channel is padded above rather than stalled: the
			// microphone has already spoken and the frame has to go out on time.
			right = right[min(FrameSize, len(right)):]

			// Blocking, not dropping. The device callbacks already discard
			// blocks when their own buffers fill, so backpressure is expressed
			// once, at the layer that cannot wait. Dropping here as well only
			// produced a burst of warnings every startup, while the consumer
			// was still being wired up.
			select {
			case s.frames <- frame:
			case <-s.done:
				return
			}
		}
	}
}
