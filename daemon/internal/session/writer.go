// Package session turns a stream of stereo frames into recordings on disk.
//
// The file is a stereo WAV: left is the microphone, right is the system audio.
// The server downmixes it to mono without being asked — faster-whisper's
// decode_audio takes split_stereo=False by default — so today's pipeline
// handles it unchanged, while the separation stays on disk for whenever
// per-channel diarization gets built.
package session

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"

	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/source"
)

// Writer streams frames into one WAV file. The header is patched on Close, so a
// recording that is killed mid-way leaves a file with a zero length rather than
// a corrupt one.
type Writer struct {
	path     string
	file     *os.File
	enc      *wav.Encoder
	buf      *audio.IntBuffer
	samples  int
	closed   sync.Once
	closeErr error
}

// Create opens a recording. The caller must Close it to get a valid file.
func Create(path string) (*Writer, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Writer{
		path: path,
		file: file,
		enc:  wav.NewEncoder(file, source.SampleRate, 16, 2, 1),
		// One buffer, reused for every frame: the encoder copies what it needs
		// and does not hold on to it, and a three-hour meeting is a lot of
		// allocations to make for nothing.
		buf: &audio.IntBuffer{
			Format:         &audio.Format{NumChannels: 2, SampleRate: source.SampleRate},
			SourceBitDepth: 16,
			Data:           make([]int, 0, 2*source.FrameSize),
		},
	}, nil
}

// Write appends one interleaved stereo frame.
func (w *Writer) Write(frame []int16) error {
	w.buf.Data = w.buf.Data[:0]
	for _, s := range frame {
		w.buf.Data = append(w.buf.Data, int(s))
	}
	if err := w.enc.Write(w.buf); err != nil {
		return fmt.Errorf("write %s: %w", w.path, err)
	}
	w.samples += len(frame) / 2
	return nil
}

// Duration is how much audio has been written so far.
func (w *Writer) Duration() time.Duration {
	return time.Duration(w.samples) * time.Second / source.SampleRate
}

// Rename moves the finished file. A recording that started as a note and turned
// out to have somebody on the other end has to change its name before the
// uploader reads the kind back out of it.
func (w *Writer) Rename(to string) error {
	if to == w.path {
		return nil
	}
	if err := os.Rename(w.path, to); err != nil {
		return err
	}
	w.path = to
	return nil
}

// Close patches the header and is safe to call twice, so a caller can close
// explicitly to check the error and still defer a close for the failure paths.
func (w *Writer) Close() error {
	w.closed.Do(func() {
		if err := w.enc.Close(); err != nil {
			w.file.Close()
			w.closeErr = fmt.Errorf("finish %s: %w", w.path, err)
			return
		}
		w.closeErr = w.file.Close()
	})
	return w.closeErr
}
