// Package engine turns a recording into named, timed turns.
//
// It is the only place that knows whisper.cpp or sherpa-onnx exist. Two
// libraries rather than one, because measuring said so: on a real Ukrainian
// meeting, sherpa's Parakeet found 106 words where Whisper found 312, and
// sherpa's diarization found the same five speakers as pyannote at twice the
// speed. So transcription is Whisper's job and speakers are sherpa's.
package engine

import (
	"errors"
	"fmt"
	"sync"
)

// Turn is one row of a transcript: what was said, when, and by whom.
type Turn struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker,omitempty"`
	Text    string  `json:"text"`
}

// Span is a stretch of audio attributed to one speaker, before any words are
// attached to it.
type Span struct {
	Start, End float64
	Speaker    int
}

// Options is what the engine needs to know beyond where its models are.
type Options struct {
	// Language of the meeting, or empty to detect it. Naming it is worth doing:
	// on a quiet passage Whisper guessed English and produced six repetitions
	// of "Thank you." where the audio was Ukrainian throughout.
	Language string

	// Threads for both libraries. Measured on diarization: one thread ran at
	// 2.1x realtime, eight at 8.6x, and the same audio through the CoreML
	// provider was slower than plain CPU.
	Threads int

	// Transcriber is "whisper" or "parakeet". See parakeet.go for why the
	// default is the one it is.
	Transcriber string
}

// Engine holds the loaded models. Loading is slow and memory-hungry, so there
// is one of these for the life of the process and its methods are safe to call
// from several goroutines.
type Engine struct {
	models string
	opts   Options

	mu       sync.Mutex // the native handles below are not reentrant
	asr      Transcriber
	speakers *speakers
	voices   *voices
}

// Transcriber is the one thing an ASR model has to do. Two implementations:
// whisper.cpp, and Parakeet through sherpa-onnx.
type Transcriber interface {
	transcribe(samples []float32) ([]Turn, error)
	close() error
}

// Open loads what is on disk. Missing models are an error rather than a silent
// downgrade: a transcript with no speakers looks like a working app that has
// quietly lost half its job.
func Open(modelsDir string, opts Options) (*Engine, error) {
	if opts.Threads <= 0 {
		opts.Threads = 8
	}
	e := &Engine{models: modelsDir, opts: opts}

	var err error
	if opts.Transcriber == Parakeet {
		e.asr, err = openParakeet(modelsDir, opts)
	} else {
		e.asr, err = openASR(modelsDir, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("transcription: %w", err)
	}
	if e.speakers, err = openSpeakers(modelsDir, opts); err != nil {
		e.asr.close()
		return nil, fmt.Errorf("speakers: %w", err)
	}
	if e.voices, err = openVoices(modelsDir, opts); err != nil {
		e.asr.close()
		e.speakers.close()
		return nil, fmt.Errorf("voices: %w", err)
	}
	return e, nil
}

// Transcribe returns what was said, with timings and no speakers.
func (e *Engine) Transcribe(samples []float32) ([]Turn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.asr.transcribe(samples)
}

// Diarize returns who spoke when, with speakers numbered rather than named.
func (e *Engine) Diarize(samples []float32) ([]Span, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.speakers.diarize(samples)
}

// Run does both and merges them, which is what a recording actually needs.
//
// Two signals, not one. heard is the mix — everybody, which is what has to be
// transcribed. apart, when there is one, is the system channel alone: the
// remote people with no room in it and, crucially, without the owner, who is on
// the microphone and known without a model. Clustering that is a strictly
// smaller problem, and measured on a meeting the owner had labelled by ear it
// is the difference between a stranger being folded into a colleague and not.
// Nothing else tried moved it — not a better segmentation model, not a better
// embedding on the mix. See exp/out/F-speakers-74.txt.
//
// The two are sample-aligned by media.Sides, so spans found in one are valid in
// the other.
func (e *Engine) Run(heard, apart []float32) (Result, error) {
	turns, err := e.Transcribe(heard)
	if err != nil {
		return Result{}, err
	}
	voices := apart
	if len(voices) == 0 {
		voices = heard // a file dropped in has no channel of its own
	}
	spans, err := e.Diarize(voices)
	if err != nil {
		// A transcript without speakers is worth keeping; failing the whole
		// recording because the speaker model stumbled is not.
		return Result{Turns: turns}, fmt.Errorf("transcribed, but speakers failed: %w", err)
	}

	e.mu.Lock()
	prints := e.voices.voiceprints(voices, spans)
	e.mu.Unlock()
	return Result{Turns: Attribute(turns, spans), Voices: prints}, nil
}

// Print is a voiceprint of one stretch of speech — the same vector the speaker
// models use to tell two people apart, for anybody who needs to ask whether two
// utterances came from the same person.
func (e *Engine) Print(samples []float32) []float32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.voices.print(samples)
}

// Solo is a recording with one person in it: transcribed, labelled, and never
// diarized.
//
// The caller knows this from the audio itself — one of our stereo recordings
// with a silent system channel is somebody talking to their own laptop, and
// asking a clustering algorithm how many people that is can only produce a
// wrong answer. It produced three, on a recording of one person, which is what
// this exists to stop.
func (e *Engine) Solo(samples []float32, who string) (Result, error) {
	turns, err := e.Transcribe(samples)
	if err != nil {
		return Result{}, err
	}
	for i := range turns {
		turns[i].Speaker = who
	}
	e.mu.Lock()
	print := e.voices.print(samples)
	e.mu.Unlock()

	out := Result{Turns: turns}
	if print != nil {
		out.Voices = map[string][]float32{who: print}
	}
	return out, nil
}

// Result is one recording, read.
type Result struct {
	Turns []Turn

	// One voiceprint per speaker label, so that SPEAKER_00 in this meeting can
	// be recognised as the person somebody named in the last one. Empty when a
	// speaker said too little to be worth a signature.
	Voices map[string][]float32
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return errors.Join(e.asr.close(), e.speakers.close(), e.voices.close())
}

// Attribute names each turn after whoever was speaking for most of it.
//
// Overlap rather than midpoint: a turn that starts while somebody else is
// finishing belongs to whoever holds most of it, and taking the midpoint gets
// that wrong exactly when two people talk over each other — which is the case
// worth getting right.
func Attribute(turns []Turn, spans []Span) []Turn {
	out := make([]Turn, len(turns))
	for i, t := range turns {
		out[i] = t
		best, who := 0.0, -1
		for _, s := range spans {
			if shared := overlap(t.Start, t.End, s.Start, s.End); shared > best {
				best, who = shared, s.Speaker
			}
		}
		if who >= 0 {
			out[i].Speaker = Label(who)
		}
	}
	return out
}

// Label is the name a speaker gets before anybody has said who they are.
func Label(speaker int) string { return fmt.Sprintf("SPEAKER_%02d", speaker) }

func overlap(aStart, aEnd, bStart, bEnd float64) float64 {
	return max(0, min(aEnd, bEnd)-max(aStart, bStart))
}
