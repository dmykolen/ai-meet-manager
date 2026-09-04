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

// transcribe runs the whole recording through Whisper.
//
// The VAD is not optional here, and this is the reason: given three minutes of
// meeting with its natural pauses, Whisper produced "Дякую." for each of the
// first three thirty-second windows and only then began transcribing. With the
// VAD on, the same audio gave thirteen real rows and no invented ones. Whisper
// writes fluent text over silence; the cure is to stop showing it silence.
func (a *asr) transcribe(samples []float32) ([]Turn, error) {
	ctx, err := a.model.NewContext()
	if err != nil {
		return nil, err
	}
	ctx.SetThreads(uint(a.opts.Threads))
	ctx.SetLanguage(language(a.opts.Language))

	ctx.SetVAD(true)
	ctx.SetVADModelPath(a.vad)
	ctx.SetVADThreshold(0.5)
	ctx.SetVADMinSpeechMs(250)
	ctx.SetVADMinSilenceMs(300)
	// A little padding either side, so a word is not clipped by the very
	// detector that is there to protect it.
	ctx.SetVADSpeechPadMs(200)

	if err := ctx.Process(samples, nil, nil, nil); err != nil {
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
		if text := strings.TrimSpace(segment.Text); text != "" {
			turns = append(turns, Turn{
				Start: segment.Start.Seconds(),
				End:   segment.End.Seconds(),
				Text:  text,
			})
		}
	}
	return turns, nil
}

// language maps the empty string to Whisper's own word for "work it out".
func language(l string) string {
	if l == "" {
		return "auto"
	}
	return l
}
