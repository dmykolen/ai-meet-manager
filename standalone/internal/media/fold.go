package media

import (
	"encoding/binary"
	"os"
	"path/filepath"
)

// Window is how often the fold reconsiders which channel to take. Twenty
// milliseconds is short enough to follow a conversation turning over and long
// enough that the decision is about speech rather than about one syllable.
const Window = Rate / 50

// Hold keeps the fold on the system channel for a moment after it goes quiet,
// so that the gap between two words does not throw the audio back into the room
// and out again. Without it, a sentence arrives chopped between two acoustics.
const Hold = 12 // windows: a quarter of a second

// Floor is the level at which the system tap counts as carrying something.
// Digital silence is exactly zero; a tap with a call on it is orders of
// magnitude above this.
const Floor = 0.002

// Fold mixes one of this app's stereo recordings down to mono by taking, at
// every moment, the channel that actually carried it.
//
// Averaging is what a stereo file usually wants and is wrong here. Without
// headphones the microphone also picks up the far side coming out of the
// speakers, so the average is a signal summed with a room recording of itself —
// comb filtering for the transcriber, and a plain audible echo for anybody
// listening back. Measured on a real meeting: the room copy was 128% the level
// of the tap and arrived half a second earlier.
//
// The rule is not "the louder channel wins", because the room copy is often the
// louder one. It is "the tap wins whenever the tap has anything", since the tap
// is a clean digital copy of exactly what the far side said and the microphone
// version is the same thing after a trip through a room.
func Fold(mic, system []float32) []float32 {
	n := min(len(mic), len(system))
	out := make([]float32, n)

	held := 0
	for at := 0; at < n; at += Window {
		to := min(at+Window, n)
		if Loud(system[at:to]) > Floor {
			held = Hold
		} else if held > 0 {
			held--
		}
		from := mic
		if held > 0 {
			from = system
		}
		copy(out[at:to], from[at:to])
	}
	return out
}

// Voices decodes a recording the way the rest of the app wants to hear it: one
// of ours folded, anything else decoded as usual.
func Voices(path string) ([]float32, error) {
	if mic, system, ok := Sides(path); ok {
		return Fold(mic, system), nil
	}
	return Decode(path)
}

// Mono writes samples as a 16 kHz mono WAV, which is what the player is given
// instead of the raw stereo file.
func Mono(path string, samples []float32) error {
	body := make([]byte, 2*len(samples))
	for i, s := range samples {
		v := int16(max(min(s, 1), -1) * 32767)
		binary.LittleEndian.PutUint16(body[2*i:], uint16(v))
	}
	head := make([]byte, 0, 44)
	head = append(head, "RIFF"...)
	head = binary.LittleEndian.AppendUint32(head, uint32(36+len(body)))
	head = append(head, "WAVEfmt "...)
	head = binary.LittleEndian.AppendUint32(head, 16)
	head = binary.LittleEndian.AppendUint16(head, 1) // PCM
	head = binary.LittleEndian.AppendUint16(head, 1) // mono
	head = binary.LittleEndian.AppendUint32(head, Rate)
	head = binary.LittleEndian.AppendUint32(head, Rate*2)
	head = binary.LittleEndian.AppendUint16(head, 2)
	head = binary.LittleEndian.AppendUint16(head, 16)
	head = append(head, "data"...)
	head = binary.LittleEndian.AppendUint32(head, uint32(len(body)))
	return os.WriteFile(path, append(head, body...), 0o644)
}

// Listenable is the file the player is served: one of ours folded to mono and
// kept in a cache beside the recordings, anything else as it is.
//
// Cached rather than folded per request, because a browser asks for an
// hour-long file in dozens of ranges and folding it each time would be absurd.
// Rebuilt whenever the original is newer, so a re-recorded file is never served
// from a stale copy.
func Listenable(path, cache string) (string, error) {
	mic, system, ok := Sides(path)
	if !ok {
		return path, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return path, err
	}
	folded := filepath.Join(cache, filepath.Base(path))

	source, err := os.Stat(path)
	if err != nil {
		return path, err
	}
	if made, err := os.Stat(folded); err == nil && made.ModTime().After(source.ModTime()) {
		return folded, nil
	}
	if err := Mono(folded, Fold(mic, system)); err != nil {
		return path, err
	}
	return folded, nil
}
