package engine

import (
	"errors"
	"path/filepath"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-macos"
)

// Threshold is how close two stretches of speech have to sound before they are
// called the same person.
//
// Chosen by measurement rather than by the library's default. On a real
// Ukrainian meeting where pyannote found five speakers, this embedding model
// found nine at 0.5, seven at 0.75 and exactly five at 0.9. Two other embedding
// models were tried on the same audio: a Chinese-trained one found nine and
// NeMo's TitaNet found sixteen, so the model matters more than the threshold.
const Threshold = 0.9

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
	// Unknown ahead of time, which is the whole point: a meeting does not
	// announce how many people are in it.
	c.Clustering.NumClusters = -1
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
