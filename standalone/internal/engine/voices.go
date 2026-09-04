package engine

import (
	"errors"
	"path/filepath"
	"sort"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-macos"
)

// Enough is how much of one person's speech is needed before their voiceprint
// is worth keeping. Below a few seconds the vector is dominated by whatever
// word happened to be said rather than by the voice saying it.
const Enough = 4.0

// voices turns a stretch of one person talking into a vector that can be
// compared with the same person in another meeting.
//
// It is the same wespeaker CAM++ model the diarizer already clusters with — the
// file is loaded twice rather than shared, because sherpa owns the handle
// inside its diarization pipeline and does not hand it out. 28 MB and about a
// second, once, for the life of the process.
type voices struct {
	ex *sherpa.SpeakerEmbeddingExtractor
}

func openVoices(dir string, opts Options) (*voices, error) {
	ex := sherpa.NewSpeakerEmbeddingExtractor(&sherpa.SpeakerEmbeddingExtractorConfig{
		Model:      filepath.Join(dir, "embedding.onnx"),
		NumThreads: opts.Threads,
		Provider:   "cpu",
	})
	if ex == nil {
		return nil, errors.New("the voice model would not load")
	}
	return &voices{ex: ex}, nil
}

// print returns one vector for a clip of a single person speaking, or nil when
// there is not enough of them to be worth remembering.
func (v *voices) print(samples []float32) []float32 {
	if v.ex == nil || float64(len(samples))/16000 < Enough {
		return nil
	}
	stream := v.ex.CreateStream()
	defer sherpa.DeleteOnlineStream(stream)

	stream.AcceptWaveform(16000, samples)
	stream.InputFinished()
	if !v.ex.IsReady(stream) {
		return nil
	}
	return v.ex.Compute(stream)
}

func (v *voices) close() error {
	if v.ex != nil {
		sherpa.DeleteSpeakerEmbeddingExtractor(v.ex)
		v.ex = nil
	}
	return nil
}

// voiceprints takes one vector per speaker in a recording.
//
// The longest stretches are used rather than all of them concatenated: a
// person's five best seconds describe their voice better than every "ага" they
// said, and it keeps the work bounded on a two-hour meeting.
func (v *voices) voiceprints(samples []float32, spans []Span) map[string][]float32 {
	bySpeaker := map[int][]Span{}
	for _, s := range spans {
		bySpeaker[s.Speaker] = append(bySpeaker[s.Speaker], s)
	}

	prints := map[string][]float32{}
	for speaker, mine := range bySpeaker {
		sort.Slice(mine, func(a, b int) bool {
			return mine[a].End-mine[a].Start > mine[b].End-mine[b].Start
		})
		var clip []float32
		for _, s := range mine {
			from, to := int(s.Start*16000), int(s.End*16000)
			if from < 0 || to > len(samples) || to <= from {
				continue
			}
			clip = append(clip, samples[from:to]...)
			if float64(len(clip))/16000 >= 3*Enough {
				break
			}
		}
		if print := v.print(clip); print != nil {
			prints[Label(speaker)] = print
		}
	}
	return prints
}
