// Package media turns any recording into the 16 kHz mono float32 the models
// want.
//
// Three decoders, tried in that order, because each is cheaper than the next.
// WAV is read here — the listener writes WAV and that is most of the traffic.
// Everything Core Audio knows (m4a, mp3, aac, alac, flac, aiff, caf, and the
// audio track of an mp4 or mov) goes through afconvert, which is part of macOS
// and needs nothing installed. What is left — webm, ogg, opus, mkv, avi — goes
// through ffmpeg, which the app fetches on first run alongside the models.
// Writing a container parser was never the job.
package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Rate is what everything downstream assumes, and nothing resamples again.
const Rate = 16000

// Tools is where the app keeps the decoders it downloaded. Set once at startup;
// an empty value just means the PATH is the only place to look.
var Tools string

// Sides splits a recording this app made into its two channels: what the
// microphone heard, and what the machine was playing.
//
// That distinction is the app's one real advantage over a generic transcriber,
// and it was being thrown away by the downmix. The microphone side is the
// person sitting here; the system side is everybody else. Anything that is not
// one of our own stereo WAVs comes back with ok false, and the caller carries
// on with the mono mix.
func Sides(path string) (mic, system []float32, ok bool) {
	if !strings.EqualFold(filepath.Ext(path), ".wav") {
		return nil, nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false
	}
	format, body, err := chunks(raw)
	if err != nil || format.channels != 2 || format.bits != 16 || int(format.rate) != Rate {
		return nil, nil, false
	}
	frames := len(body) / 4
	mic, system = make([]float32, frames), make([]float32, frames)
	for i := range frames {
		mic[i] = float32(int16(binary.LittleEndian.Uint16(body[4*i:]))) / 32768
		system[i] = float32(int16(binary.LittleEndian.Uint16(body[4*i+2:]))) / 32768
	}
	return mic, system, true
}

// Loud is the RMS of a stretch of samples, which is all anything here needs to
// ask about a channel.
func Loud(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// Decode reads a file and returns mono samples at Rate.
//
// Stereo is mixed down rather than refused: the listener records the microphone
// on the left and the system audio on the right, and the models want one signal.
// Keeping both channels on disk is what makes per-speaker work possible later.
func Decode(path string) ([]float32, error) {
	if strings.EqualFold(filepath.Ext(path), ".wav") {
		samples, err := decodeWAV(path)
		if err == nil {
			return samples, nil
		}
		// An unusual WAV — 24-bit, a-law, an odd chunk layout — is a decoder's
		// problem rather than ours.
	}

	var failed []string
	for _, decode := range []struct {
		name string
		run  func(string) ([]float32, error)
	}{
		{"afconvert", viaAfconvert},
		{"ffmpeg", viaFFmpeg},
	} {
		samples, err := decode.run(path)
		if err == nil {
			return samples, nil
		}
		failed = append(failed, decode.name+": "+trim(err.Error()))
	}
	return nil, fmt.Errorf("%s could not be read (%s)", filepath.Base(path), strings.Join(failed, "; "))
}

// trim keeps an error short enough to show on a card. ffmpeg in particular
// writes a paragraph about the file before saying what went wrong.
func trim(s string) string {
	if line := strings.TrimSpace(strings.Split(s, "\n")[0]); len(line) > 90 {
		return line[:90] + "…"
	} else {
		return line
	}
}

// viaAfconvert uses the converter that ships with macOS. It reads everything
// Core Audio does, which is most of what a meeting is ever recorded as, and it
// is on every Mac — no download, no bundling, no licence to think about.
//
// It writes a file rather than a stream, so the result goes to a temporary WAV
// and comes back through the reader above.
func viaAfconvert(path string) ([]float32, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("only on macOS")
	}
	out, err := os.CreateTemp("", "mt-*.wav")
	if err != nil {
		return nil, err
	}
	out.Close()
	defer os.Remove(out.Name())

	cmd := exec.Command("/usr/bin/afconvert",
		"-f", "WAVE", "-d", fmt.Sprintf("LEI16@%d", Rate), "-c", "1", path, out.Name())
	if raw, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(raw)))
	}
	return decodeWAV(out.Name())
}

// viaFFmpeg is the catch-all: webm, ogg, opus, mkv, avi, wmv, and anything else
// somebody hands the app.
func viaFFmpeg(path string) ([]float32, error) { return decodeWith(path, "ffmpeg") }

// decodeWAV handles the ordinary case without spawning anything: 16-bit PCM,
// any channel count, any sample rate that is a multiple of ours.
func decodeWAV(path string) ([]float32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	format, data, err := chunks(raw)
	if err != nil {
		return nil, err
	}
	if format.audioFormat != 1 || format.bits != 16 {
		return nil, fmt.Errorf("wav: format %d at %d bits is not plain 16-bit PCM", format.audioFormat, format.bits)
	}
	if format.rate%Rate != 0 {
		return nil, fmt.Errorf("wav: %d Hz does not divide down to %d", format.rate, Rate)
	}

	step := int(format.rate / Rate)
	channels := int(format.channels)
	frames := len(data) / 2 / channels
	out := make([]float32, 0, frames/step)

	// Mix the channels and take every step'th frame. Decimation without a filter
	// is crude, but the listener already writes 16 kHz, so this path is for the
	// occasional 48 kHz file rather than the common case.
	for f := 0; f < frames; f += step {
		var sum float32
		for c := range channels {
			at := (f*channels + c) * 2
			sum += float32(int16(binary.LittleEndian.Uint16(data[at:]))) / 32768
		}
		out = append(out, sum/float32(channels))
	}
	return out, nil
}

type wavFormat struct {
	audioFormat uint16
	channels    uint16
	rate        uint32
	bits        uint16
}

// chunks walks the RIFF structure instead of assuming a 44-byte header, because
// plenty of encoders put a LIST or a fact chunk in front of the data.
func chunks(raw []byte) (wavFormat, []byte, error) {
	var format wavFormat
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return format, nil, errors.New("wav: not a RIFF/WAVE file")
	}
	seen := false
	for at := 12; at+8 <= len(raw); {
		id := string(raw[at : at+4])
		size := int(binary.LittleEndian.Uint32(raw[at+4 : at+8]))
		body := at + 8
		if body+size > len(raw) {
			size = len(raw) - body // a truncated final chunk is still readable
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return format, nil, errors.New("wav: short format chunk")
			}
			format = wavFormat{
				audioFormat: binary.LittleEndian.Uint16(raw[body:]),
				channels:    binary.LittleEndian.Uint16(raw[body+2:]),
				rate:        binary.LittleEndian.Uint32(raw[body+4:]),
				bits:        binary.LittleEndian.Uint16(raw[body+14:]),
			}
			seen = true
		case "data":
			if !seen {
				return format, nil, errors.New("wav: data before format")
			}
			return format, raw[body : body+size], nil
		}
		at = body + size + size%2 // chunks are word-aligned
	}
	return format, nil, errors.New("wav: no data chunk")
}

// decodeWith runs ffmpeg and reads raw samples from its stdout.
func decodeWith(path, tool string) ([]float32, error) {
	bin, err := find(tool)
	if err != nil {
		return nil, fmt.Errorf("not installed")
	}
	cmd := exec.Command(bin,
		"-nostdin", "-loglevel", "error",
		"-i", path,
		"-f", "f32le", "-acodec", "pcm_f32le",
		"-ac", "1", "-ar", fmt.Sprint(Rate),
		"-")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %s", filepath.Base(path), strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	samples := make([]float32, len(out)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(out[4*i:]))
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("%s has no audio we can read", filepath.Base(path))
	}
	return samples, nil
}

// find prefers the app's own copy, so a machine with an ancient ffmpeg on its
// PATH does not get to decide what this app can open.
func find(tool string) (string, error) {
	if Tools != "" {
		if candidate := filepath.Join(Tools, tool); usable(candidate) {
			return candidate, nil
		}
	}
	if self, err := os.Executable(); err == nil {
		for _, candidate := range []string{
			filepath.Join(filepath.Dir(self), tool),
			filepath.Join(filepath.Dir(self), "..", "Resources", tool),
		} {
			if usable(candidate) {
				return filepath.Abs(candidate)
			}
		}
	}
	return exec.LookPath(tool)
}

func usable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
