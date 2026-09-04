package listen

import (
	"fmt"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-macos"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/audio"
)

// Ears is one channel's answer to "is somebody talking right now".
//
// Silero rather than WebRTC's detector: WebRTC's was built to find silence and
// is weak on music, and this app's whole job is ignoring sounds that are not a
// conversation. It runs through sherpa-onnx, which is already linked in for the
// speaker models — the alternative was a second ONNX runtime and a 41 MB
// library shipped beside the binary for a 600 KB model.
//
// Using sherpa also sidesteps the trap that cost a day in the daemon: Silero
// does not score a bare 512-sample hop, it wants the previous hop's last 64
// samples in front of it, and feeding it 512 makes real speech score 0.003.
// sherpa's own wrapper carries that context, so nothing here has to.
type Ears struct {
	vad     *sherpa.VoiceActivityDetector
	samples []float32
}

// Listen loads the detector. One per channel: the model is recurrent, so the
// microphone and the system audio each need their own.
func Listen(model string) (*Ears, error) {
	cfg := &sherpa.VadModelConfig{
		SampleRate: audio.SampleRate,
		NumThreads: 1,
		Provider:   "cpu",
	}
	cfg.SileroVad.Model = model
	cfg.SileroVad.Threshold = 0.5
	cfg.SileroVad.WindowSize = audio.FrameSize
	// Shorter than sherpa's own defaults on purpose. This is not the thing that
	// decides a meeting is over — the detector's own quiet period does that,
	// and it is measured in minutes. All that is wanted here is a per-frame
	// answer that does not flicker between words.
	cfg.SileroVad.MinSpeechDuration = 0.1
	cfg.SileroVad.MinSilenceDuration = 0.25
	// Long, because a segment ending is of no interest: nothing reads the
	// segments, only whether speech is happening.
	cfg.SileroVad.MaxSpeechDuration = 60

	// One second of buffer. The segments it collects are dropped on every
	// frame, so this only has to be larger than one hop.
	vad := sherpa.NewVoiceActivityDetector(cfg, 1)
	if vad == nil {
		return nil, fmt.Errorf("could not load the speech detector from %s", model)
	}
	return &Ears{vad: vad, samples: make([]float32, audio.FrameSize)}, nil
}

// Speaking reports whether this frame is inside speech, and hands back any
// utterance that finished on it. The frame must be exactly audio.FrameSize
// samples, which every buffer in this package is built from in the first place.
//
// The utterance is the reason the detector earns its keep twice: the same pass
// that decides a meeting is happening also cuts the audio at the places a
// person stopped talking, which is exactly what a transcriber wants to be fed.
func (e *Ears) Speaking(frame []int16) (bool, []Utterance, error) {
	if len(frame) != audio.FrameSize {
		return false, nil, fmt.Errorf("vad: got %d samples, want %d", len(frame), audio.FrameSize)
	}
	for i, s := range frame {
		e.samples[i] = float32(s) / 32768
	}
	e.vad.AcceptWaveform(e.samples)

	var done []Utterance
	for !e.vad.IsEmpty() {
		if seg := e.vad.Front(); seg != nil && len(seg.Samples) > 0 {
			done = append(done, Utterance{
				At:      float64(seg.Start) / audio.SampleRate,
				Samples: seg.Samples,
			})
		}
		e.vad.Pop()
	}
	return e.vad.IsSpeech(), done, nil
}

// An Utterance is one stretch of somebody talking, cut where they stopped.
type Utterance struct {
	At      float64 // seconds from when this detector started listening
	Samples []float32
}

// Forget drops the conversation so far, so that the tail of one recording
// cannot colour the start of the next.
func (e *Ears) Forget() { e.vad.Reset() }

func (e *Ears) Close() { sherpa.DeleteVoiceActivityDetector(e.vad) }
