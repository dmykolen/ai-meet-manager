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

// Breath is the pause between words that ends a phrase. Under a fifth of a
// second is the gap inside a sentence; over it, somebody has finished a thought.
const Breath = 0.45

// transcribe runs the whole recording through Whisper.
//
// The VAD is not optional: given three minutes of meeting with natural pauses,
// Whisper produced "Дякую." for each of the first three windows before it began
// transcribing. It writes fluent text over silence, so it is not shown silence.
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

	// Per-token times and probabilities. The times put a word boundary where
	// the word is instead of rounding a whole segment to the nearest second;
	// the probabilities are what believable judges a row on.
	ctx.SetTokenTimestamps(true)

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
		turns = append(turns, phrases(segment)...)
	}
	return turns, nil
}

// phrases breaks one of Whisper's segments where a person would break it.
//
// Whisper returns whatever fitted its thirty-second window, which is a unit of
// decoding rather than of speech: one row can hold three sentences and the next
// half of a fourth. Splitting on a character count — the usual cure — moves the
// break somewhere else arbitrary. This splits at the end of a sentence, or at a
// pause long enough to be one.
//
// Only ever between words. Whisper's tokens are word pieces and its per-token
// times, without DTW, are rough enough to be non-monotonic; splitting on them
// blindly produced rows reading "Р", "оз", "слабиться". A break is legal only
// where the next token starts a new word, which its leading space marks.
//
// Rows the model is not confident it heard are dropped rather than shown.
func phrases(segment whisper.Segment) []Turn {
	whole := strings.TrimSpace(segment.Text)
	if whole == "" {
		return nil
	}
	from, to := segment.Start.Seconds(), segment.End.Seconds()

	words := spoken(segment.Tokens)
	if len(words) < 2 {
		return []Turn{{Start: from, End: to, Text: whole}}
	}

	var out []Turn
	var text strings.Builder
	var odds []float32
	start, end := from, from

	flush := func() {
		said := strings.TrimSpace(text.String())
		text.Reset()
		if said != "" && believable(odds) {
			out = append(out, Turn{Start: start, End: max(end, start), Text: said})
		}
		odds = odds[:0]
	}

	for i, w := range words {
		if text.Len() == 0 {
			start = w.at
		}
		text.WriteString(w.text)
		odds = append(odds, w.odds...)
		end = w.until

		last := i+1 == len(words)
		gap := 0.0
		if !last {
			gap = words[i+1].at - w.until
		}
		if last || ends(w.text) || gap > Breath {
			flush()
		}
	}
	flush()
	if len(out) == 0 {
		return nil
	}
	return out
}

// word is one word of a segment: its text, when it was said, and how sure the
// model was of each piece it is made of.
type word struct {
	text      string
	at, until float64
	odds      []float32
}

// spoken groups Whisper's tokens into words and throws the control tokens away.
// Times are forced forward: without DTW they can run backwards, and a row that
// ends before it starts is worse than one that is a little late.
func spoken(tokens []whisper.Token) []word {
	var out []word
	clock := 0.0
	for _, t := range tokens {
		if t.Id >= specials || strings.TrimSpace(t.Text) == "" && t.Text != " " {
			continue
		}
		at := max(t.Start.Seconds(), clock)
		until := max(t.End.Seconds(), at)
		clock = until

		// A leading space is how Whisper marks the start of a word.
		if len(out) == 0 || strings.HasPrefix(t.Text, " ") {
			out = append(out, word{text: t.Text, at: at, until: until, odds: []float32{t.P}})
			continue
		}
		last := &out[len(out)-1]
		last.text += t.Text
		last.until = until
		last.odds = append(last.odds, t.P)
	}
	return out
}

// specials is where whisper's vocabulary stops being words. Everything at or
// above it is a control token and has no place in a transcript.
const specials = 50257

// ends reports whether a token finishes a thought.
func ends(token string) bool {
	t := strings.TrimSpace(token)
	if t == "" {
		return false
	}
	return strings.ContainsAny(t[len(t)-1:], ".!?…")
}

// language maps the empty string to Whisper's own word for "work it out".
func language(l string) string {
	if l == "" {
		return "auto"
	}
	return l
}
