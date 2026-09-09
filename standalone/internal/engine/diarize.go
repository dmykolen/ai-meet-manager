package engine

import (
	"errors"
	"path/filepath"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-macos"
)

// Threshold is how alike two stretches must sound to be one person. Loose on
// purpose: split freely here, rejoin by voiceprint in internal/library.
//
// Measured on two recordings whose answers are known — speakers found at each
// threshold, against 2 and 6:
//
//	0.90 → 8, 9    1.00 → 5, 5    1.10 → 2 ✓, 3 ✗    1.20 → 2, 2
//
// No value serves both, so the choice is which mistake to make. Splitting one
// person in two is undone later by store.Same; merging two into one destroys
// the distinction for good. Stronger embedding models were tried and were
// worse: resnet221_LM and resnet293_LM merge three known people into one.
const Threshold = 0.90

// speakers is sherpa-onnx: pyannote segmentation, then embeddings, then
// clustering. Measured at 24x realtime against pyannote's 10.5x on the GPU.
type speakers struct {
	sd *sherpa.OfflineSpeakerDiarization
}

func openSpeakers(dir string, opts Options) (*speakers, error) {
	var c sherpa.OfflineSpeakerDiarizationConfig
	c.Segmentation.Pyannote.Model = filepath.Join(dir, "segmentation", "model.onnx")
	c.Segmentation.NumThreads = opts.Threads
	c.Embedding.Model = filepath.Join(dir, "embedding.onnx")
	c.Embedding.NumThreads = opts.Threads
	c.Clustering.NumClusters = -1 // a meeting does not announce how many are in it
	c.Clustering.Threshold = Threshold
	c.MinDurationOn = 0.3
	c.MinDurationOff = 0.5

	sd := sherpa.NewOfflineSpeakerDiarization(&c)
	if sd == nil {
		return nil, errors.New("the speaker models would not load")
	}
	return &speakers{sd: sd}, nil
}

func (s *speakers) diarize(samples []float32) ([]Span, error) {
	if s.sd == nil {
		return nil, errors.New("no speaker model")
	}
	segments := s.sd.Process(samples)
	spans := make([]Span, len(segments))
	for i, seg := range segments {
		spans[i] = Span{Start: float64(seg.Start), End: float64(seg.End), Speaker: seg.Speaker}
	}
	return spans, nil
}

func (s *speakers) close() error {
	if s.sd != nil {
		sherpa.DeleteOfflineSpeakerDiarization(s.sd)
		s.sd = nil
	}
	return nil
}
