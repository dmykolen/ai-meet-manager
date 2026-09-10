package engine

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	whisper "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"
)

// asr is whisper.cpp, on Metal where there is one.
type asr struct {
	model whisper.Model
	vad   string
	opts  Options
}

func openASR(dir string, opts Options) (*asr, error) {
	path := filepath.Join(dir, "whisper.bin")
	model, err := whisper.New(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &asr{model: model, vad: filepath.Join(dir, "vad.bin"), opts: opts}, nil
}

func (a *asr) close() error {
	if a.model == nil {
		return nil
	}
	return a.model.Close()
}

// Rate is what Whisper is fed and what everything upstream produces.
const Rate = 16000

// transcribe runs the whole recording through Whisper, a row per segment.
//
// The VAD is not optional: given three minutes of meeting with natural pauses,
// Whisper produced "Дякую." for each of the first three windows before it began
// transcribing. It writes fluent text over silence, so it is not shown silence.
//
// A row is a segment and nothing smaller, and that is the part worth knowing
// about. Rows used to be cut out of Whisper's per-token times, at sentence ends
// and long pauses, which read better and was wrong: the VAD deletes the silence
// before the model ever sees the audio, so everything the model says is on a
// shorter clock. whisper.cpp maps SEGMENT times back to the recording for you
// — whisper_full_get_segment_t0 — and offers mapped token times too, but the Go
// binding does not call them; it reads the raw struct fields, which are still
// on the short clock. Measured on a 167-second meeting: the segments ended at
// 167.34 and the tokens at 143.48, and a word landed a median 12.3 s from where
// it was said. Rebuilt from segment times that becomes 0.35 s, and the
// speakers, which are matched to rows by overlap, go from 47% right to 66%.
// See exp/out/I-timeline.txt and exp/out/J-rows.txt.
//
// Splitting the segments again was measured and bought nothing — 0.35 s against
// 0.32 s, for eighty lines and a row reading "писати." on its own — so the rows
// are Whisper's own and the code that cut them is gone.
func (a *asr) transcribe(samples []float32) ([]Turn, error) {
	ctx, err := a.model.NewContext()
	if err != nil {
		return nil, err
	}
	ctx.SetThreads(uint(a.opts.Threads))
	ctx.SetLanguage(language(a.opts.Language))

	// Whisper is autoregressive and is fed its own previous output as context,
	// so a phrase emitted twice makes a third copy likelier and it locks: one
	// meeting came back with "Данію." ten times in a row. Zero is whisper.cpp's
	// name for condition_on_previous_text = False, and on that meeting it took
	// ten repetitions to none while running a quarter faster. A temperature
	// fallback and beam search were measured on the same audio and added
	// nothing this does not already fix.
	ctx.SetMaxContext(0)

	ctx.SetVAD(true)
	ctx.SetVADModelPath(a.vad)
	ctx.SetVADThreshold(0.5)
	ctx.SetVADMinSpeechMs(250)
	ctx.SetVADMinSilenceMs(300)
	// A little padding either side, so a word is not clipped by the very
	// detector that is there to protect it.
	ctx.SetVADSpeechPadMs(200)

	if err := ctx.Process(prepare(samples, Rate), nil, nil, nil); err != nil {
		return nil, err
	}

	var turns []Turn
	for {
		segment, err := ctx.NextSegment()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		text := strings.TrimSpace(segment.Text)
		if text == "" || !believable(odds(segment.Tokens)) {
			continue
		}
		turns = append(turns, Turn{
			Start: segment.Start.Seconds(),
			End:   segment.End.Seconds(),
			Text:  text,
		})
	}
	return turns, nil
}

// odds is how sure the model was of each piece of a segment, which is what
// believable judges a row on. Per-token times are not asked for and not used —
// measured to change nothing about the rows now that they are Whisper's own.
func odds(tokens []whisper.Token) []float32 {
	out := make([]float32, 0, len(tokens))
	for _, t := range tokens {
		if t.Id < specials {
			out = append(out, t.P)
		}
	}
	return out
}

// specials is where whisper's vocabulary stops being words. Everything at or
// above it is a control token and has no confidence worth averaging in.
const specials = 50257

// language maps the empty string to Whisper's own word for "work it out".
func language(l string) string {
	if l == "" {
		return "auto"
	}
	return l
}
