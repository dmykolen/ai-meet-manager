package engine

import (
	"errors"
	"path/filepath"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-macos"
)

// parakeet is NVIDIA's Parakeet TDT 0.6B v3, through sherpa-onnx. Offered, not
// recommended.
//
// Measured against Whisper on the same 23-minute meeting: 11 rows to Whisper's
// 281, and largely nonsense. On three minutes of another it managed 161 words
// to Whisper's 320, and in Russian — the transducer takes no language argument,
// so there is nothing to set that stops it.
//
// Kept because it cannot loop, being non-autoregressive, and because NVIDIA's
// own numbers put it ahead on clean read speech. A headset is not a room.
type parakeet struct {
	rec *sherpa.OfflineRecognizer
}

// Parakeet is the name this engine goes by in the settings and on disk.
const Parakeet = "parakeet"

func openParakeet(dir string, opts Options) (*parakeet, error) {
	models := filepath.Join(dir, Parakeet)

	var c sherpa.OfflineRecognizerConfig
	c.ModelConfig.Transducer.Encoder = filepath.Join(models, "encoder.int8.onnx")
	c.ModelConfig.Transducer.Decoder = filepath.Join(models, "decoder.int8.onnx")
	c.ModelConfig.Transducer.Joiner = filepath.Join(models, "joiner.int8.onnx")
	c.ModelConfig.Tokens = filepath.Join(models, "tokens.txt")
	c.ModelConfig.ModelType = "nemo_transducer"
	c.ModelConfig.NumThreads = opts.Threads
	c.ModelConfig.Provider = "cpu"
	c.DecodingMethod = "greedy_search"

	rec := sherpa.NewOfflineRecognizer(&c)
	if rec == nil {
		return nil, errors.New("the Parakeet model would not load; it may still be downloading")
	}
	return &parakeet{rec: rec}, nil
}

func (p *parakeet) close() error {
	if p.rec != nil {
		sherpa.DeleteOfflineRecognizer(p.rec)
		p.rec = nil
	}
	return nil
}

// Chunk is how much audio Parakeet is given at a time.
//
// The whole recording in one call works and is fastest, but a transducer holds
// its whole hypothesis in memory and an hour of meeting is 3.5 million samples;
// splitting it keeps that bounded and gives the rows something to hang
// timestamps on. Two minutes, because the model has no sentence context beyond
// what it is fed and cutting more finely costs accuracy at every seam.
const Chunk = 120.0

// transcribe returns rows with a timestamp per chunk.
//
// Coarser than Whisper's, which knows where each word was. sherpa's offline
// transducer hands back the text of what it was given and nothing about when —
// so a row here covers two minutes, and the speaker attribution that follows is
// correspondingly rougher. Another reason this is not the default.
func (p *parakeet) transcribe(samples []float32) ([]Turn, error) {
	if p.rec == nil {
		return nil, errors.New("no transcription model")
	}
	var turns []Turn
	step := int(Chunk * 16000)

	for at := 0; at < len(samples); at += step {
		to := min(at+step, len(samples))
		stream := sherpa.NewOfflineStream(p.rec)
		stream.AcceptWaveform(16000, samples[at:to])
		p.rec.Decode(stream)
		text := strings.TrimSpace(stream.GetResult().Text)
		sherpa.DeleteOfflineStream(stream)

		if text != "" {
			turns = append(turns, Turn{
				Start: float64(at) / 16000,
				End:   float64(to) / 16000,
				Text:  text,
			})
		}
	}
	return turns, nil
}
