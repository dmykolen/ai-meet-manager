// Package models fetches the model files on first run.
//
// They are not in the binary and never will be: together they are the better
// part of a gigabyte, they change on their own schedule, and a 900 MB download
// that fails halfway should be resumable rather than a reinstall. So the binary
// stays small enough to hand somebody, and the first launch spends a few
// minutes filling ~/MeetingTranscriber/models.
package models

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// A Model is one downloadable thing. Archives are unpacked; single files are
// saved as they are.
type Model struct {
	Name  string // what a person sees while it downloads
	Key   string // the folder or file it becomes, under models/
	URL   string
	Bytes int64  // for the progress bar, and to notice a truncated download
	Check string // sha256 of the download, when it is worth pinning
	Run   bool   // an executable rather than weights: unpacked into bin/, +x
}

// Everything the app needs to work offline. Sizes are the compressed download.
//
// The speaker models were chosen by measurement, not by reputation: on a real
// Ukrainian meeting, wespeaker CAM++ at a 0.9 clustering threshold found the
// same five speakers pyannote did, at 24x realtime, while a Chinese-trained
// embedder found twenty-four and NeMo's TitaNet found sixteen.
var (
	// Whisper, quantised. Measured against the full 1.5 GB model on the same
	// Ukrainian meeting: 170 words against 173, at identical speed. Unlike
	// Parakeet's int8 — which lost two thirds of the speech — q5_0 costs
	// almost nothing, and saves a gigabyte of download.
	Whisper = Model{
		Name:  "Transcription",
		Key:   "whisper.bin",
		URL:   "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin",
		Bytes: 574_000_000,
	}

	// Whisper's own VAD. Not optional: without it, three minutes of meeting
	// with natural pauses produced "Дякую." for each of the first three
	// thirty-second windows before any real text appeared.
	Vad = Model{
		Name:  "Speech detection",
		Key:   "vad.bin",
		URL:   "https://huggingface.co/ggml-org/whisper-vad/resolve/main/ggml-silero-v5.1.2.bin",
		Bytes: 885_000,
	}

	// Silero, for deciding second by second whether anybody is talking. This is
	// the one the always-on part runs on, and it is a different model from Vad
	// above: that one is ggml, built into whisper.cpp, and answers about a
	// finished file. This one is ONNX, runs through sherpa-onnx — already
	// linked in for the speaker models — and answers about the last 32 ms.
	Speech = Model{
		Name:  "Listening",
		Key:   "silero_vad.onnx",
		URL:   "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx",
		Bytes: 643_854,
	}

	// ffmpeg, for the containers Core Audio cannot open: webm, ogg, opus, mkv,
	// avi. macOS reads m4a, mp3, aac, flac and the audio track of an mp4 through
	// afconvert with nothing installed, which is most of what a meeting is ever
	// recorded as — but "most" is not "any", and a person holding a file the app
	// refuses does not care which library was missing.
	//
	// Fetched rather than bundled, for the same reason the models are: it is
	// 45 MB unpacked, and the binary stays small enough to hand somebody. The
	// hash is pinned because this is the one download that is executed.
	FFmpeg = Model{
		Name:  "Format support",
		Key:   "ffmpeg",
		URL:   "https://github.com/eugeneware/ffmpeg-static/releases/download/b6.1.1/ffmpeg-darwin-arm64.gz",
		Bytes: 19_246_198,
		Check: "8923876afa8db5585022d7860ec7e589af192f441c56793971276d450ed3bbfa",
		Run:   true,
	}

	// Parakeet, for anybody who wants to try it. Not in Required(): 670 MB is
	// a lot to download for an engine the measurements say to leave off, so it
	// arrives when it is chosen and not before.
	ParakeetModel = Model{
		Name:  "Parakeet",
		Key:   "parakeet",
		URL:   "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2",
		Bytes: 636_000_000,
	}

	Segmentation = Model{
		Name:  "Speaker segmentation",
		Key:   "segmentation",
		URL:   "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/sherpa-onnx-pyannote-segmentation-3-0.tar.bz2",
		Bytes: 7_000_000,
	}

	// Chosen by measurement. On a meeting where pyannote found five speakers,
	// this found exactly five; a Chinese-trained embedder found nine and
	// NeMo's TitaNet sixteen, on the same audio at the same threshold.
	Embedding = Model{
		Name:  "Speaker voices",
		Key:   "embedding.onnx",
		URL:   "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/wespeaker_en_voxceleb_CAM++.onnx",
		Bytes: 28_000_000,
	}
)

// Optional is what a particular choice needs on top of Required.
func Optional(transcriber string) Set {
	if transcriber == "parakeet" {
		return Set{ParakeetModel}
	}
	return nil
}

// Everything a working install needs, in the order it is fetched: the small
// ones first, so that a slow connection shows progress early.
func Required() Set { return Set{Vad, Speech, FFmpeg, Segmentation, Embedding, Whisper} }

// Progress is what the first-run screen watches.
type Progress struct {
	Model      string
	Done, Size int64
	Finished   bool
	Err        error
}

// Fraction is 0..1, or 0 when the size is not known.
func (p Progress) Fraction() float64 {
	if p.Size <= 0 {
		return 0
	}
	return min(float64(p.Done)/float64(p.Size), 1)
}

// Set is the group of models a running configuration needs.
type Set []Model

// Missing returns the members of the set that are not on disk yet, so that a
// second launch costs nothing and an interrupted first launch resumes.
func (s Set) Missing(dir string) Set {
	var todo Set
	for _, m := range s {
		if !Have(dir, m) {
			todo = append(todo, m)
		}
	}
	return todo
}

// Size is the total download still to do.
func (s Set) Size() int64 {
	var total int64
	for _, m := range s {
		total += m.Bytes
	}
	return total
}

// Have reports whether a model is already unpacked. A marker file is written
// last, so a half-unpacked archive is never mistaken for a finished one.
func Have(dir string, m Model) bool {
	_, err := os.Stat(filepath.Join(dir, ".have-"+m.Key))
	return err == nil
}

// Path is where a model ended up. Executables go in a sibling bin/ so that
// nothing ever has to guess whether a file under models/ can be run.
func Path(dir string, m Model) string {
	if m.Run {
		return filepath.Join(Tools(dir), m.Key)
	}
	return filepath.Join(dir, m.Key)
}

// Tools is the folder the downloaded executables live in.
func Tools(dir string) string { return filepath.Join(filepath.Dir(dir), "bin") }

// Fetch downloads and unpacks everything missing, reporting as it goes.
//
// The channel is closed when the last model is done. A failure stops the run
// and is delivered on the channel rather than returned, because the caller is a
// progress screen and not a `go build`.
func Fetch(ctx context.Context, dir string, set Set, report chan<- Progress) {
	defer close(report)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		report <- Progress{Err: err}
		return
	}
	for _, m := range set {
		if err := fetch(ctx, dir, m, report); err != nil {
			report <- Progress{Model: m.Name, Err: err}
			return
		}
		report <- Progress{Model: m.Name, Done: m.Bytes, Size: m.Bytes, Finished: true}
	}
}

func fetch(ctx context.Context, dir string, m Model, report chan<- Progress) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", m.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: the download returned %s", m.Name, resp.Status)
	}

	size := resp.ContentLength
	if size <= 0 {
		size = m.Bytes
	}
	counted := &counter{reader: resp.Body}

	// Hashed as it arrives rather than re-read afterwards: the file may be
	// 574 MB, and one of these downloads is executed, so "pinned" has to mean
	// checked and not merely written down.
	var digest io.Reader = counted
	sum := sha256.New()
	if m.Check != "" {
		digest = io.TeeReader(counted, sum)
	}

	// The reader is watched from here rather than from inside the copy, so that
	// unpacking a 600 MB archive still moves the bar.
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				report <- Progress{Model: m.Name, Done: counted.read.Load(), Size: size}
			}
		}
	}()
	defer close(done)

	if err := unpack(digest, dir, m); err != nil {
		return fmt.Errorf("%s: %w", m.Name, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); m.Check != "" && got != m.Check {
		_ = os.RemoveAll(Path(dir, m))
		return fmt.Errorf("%s: the download does not match its published checksum "+
			"(expected %s, got %s) and has been discarded", m.Name, m.Check[:12], got[:12])
	}
	// Last, and only now: this is what Have looks for.
	return os.WriteFile(filepath.Join(dir, ".have-"+m.Key), []byte(m.URL), 0o644)
}

// unpack writes a single file straight through, and expands an archive into a
// folder named after the model rather than after whatever the tarball happens
// to call its top directory.
func unpack(r io.Reader, dir string, m Model) error {
	switch {
	case m.Run:
		gz, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(Tools(dir), 0o755); err != nil {
			return err
		}
		if err := writeFile(Path(dir, m), gz); err != nil {
			return err
		}
		return os.Chmod(Path(dir, m), 0o755)
	case !strings.HasSuffix(m.URL, ".tar.bz2"):
		return writeFile(filepath.Join(dir, m.Key), r)
	}

	into := filepath.Join(dir, m.Key)
	if err := os.RemoveAll(into); err != nil {
		return err
	}
	archive := tar.NewReader(bzip2.NewReader(r))
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		// Drop the archive's own top-level directory, and refuse anything that
		// tries to climb out of ours.
		name := strip(header.Name)
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		if err := writeFile(filepath.Join(into, name), archive); err != nil {
			return err
		}
	}
}

func strip(name string) string {
	if _, rest, found := strings.Cut(filepath.Clean(name), string(filepath.Separator)); found {
		return rest
	}
	return ""
}

func writeFile(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, r)
	return err
}

// counter counts bytes on their way past, for the progress bar.
type counter struct {
	reader io.Reader
	read   atomic.Int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.read.Add(int64(n))
	return n, err
}

// Sum is the sha256 of a file, for pinning a model once its hash is known.
func Sum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
