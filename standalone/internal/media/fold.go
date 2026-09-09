package media

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
)

// Window is how often the mix decides whose moment this is: twenty
// milliseconds, which is shorter than any syllable.
const Window = Rate / 50

// Floor is the quietest a channel can be and still count as somebody talking.
const Floor = 0.002

// Louder is how far the microphone must beat the far side to own the moment.
// The room copy is always well under the tap that made it, so a microphone
// merely as loud as the tap is the room and not a person. Generous on purpose:
// cutting somebody off mid-sentence is the worse mistake.
const Louder = 3.0

// Slide is how long the mix takes to change hands. Long enough that nothing
// clicks, short enough that no syllable is handed to the wrong channel.
const Slide = 30 * Rate / 1000

// Fold_ names the version of the mix that made a cached copy. Bump it whenever
// the sound of Fold changes, or everybody goes on listening to the old one.
const Fold_ = "v4-"

// Fold mixes one of our stereo recordings to mono by switching, not summing.
//
// Summing put every remote sentence in twice — once from the tap, once from the
// room — and ducking the microphone only made the second copy quieter. Measured
// on a 23-minute meeting: ducking 12x harder bought 2 dB and cost the owner's
// own voice 1.8 dB. Switching gives each instant to whoever owns it, so every
// voice is in the mix once by construction: -19.1 dB of leftover copy became
// -23.3 dB, and the owner's voice went from -1.8 dB to 0.0.
//
// Requires channels that line up. Sides does that; without it a switch changes
// hands a quarter of a second late and is worse than the sum was.
func Fold(mic, system []float32) []float32 {
	n := min(len(mic), len(system))
	out := make([]float32, n)

	// 1 is the microphone's, 0 is the tap's. It starts on the microphone
	// because a recording that begins with the owner speaking is the common
	// case and there is nothing to fade from.
	at := float32(1)
	step := float32(1) / float32(Slide)

	for i := 0; i < n; i += Window {
		to := min(i+Window, n)
		them, you := Loud(system[i:to]), Loud(mic[i:to])

		want := float32(1)
		if them > Floor && you < them*Louder {
			want = 0
		}
		for k := i; k < to; k++ {
			switch {
			case at < want:
				at = min(at+step, want)
			case at > want:
				at = max(at-step, want)
			}
			// Linear, not equal-power: equal-power puts 0.71 of each into the
			// mix at the midpoint and can clip. The dip it protects against
			// needs both channels loud at once, and a handover means one of
			// them has already gone quiet.
			out[k] = mic[k]*at + system[k]*(1-at)
		}
	}
	return out
}

// Offset is how many samples the system tap sits behind the microphone.
//
// A property of the recorder, not the room: audiotee's stream is interleaved
// late, by a steady amount for a whole recording. Measured on a real meeting at
// 230 ms. The search runs both ways on purpose — forbidding a negative answer,
// on the reasoning that a room cannot hear a sound before it is played, kept it
// out of the half-plane the answer was in.
func Offset(mic, system []float32) int {
	// The loudest ten seconds of the far side: the one stretch where the same
	// sound is certainly in both channels.
	window := min(10*Rate, len(system), len(mic))
	if window < Rate {
		return 0
	}
	best, from := 0.0, 0
	for s := 0; s+window <= len(system); s += max(window/2, 1) {
		if l := Loud(system[s : s+window]); l > best {
			best, from = l, s
		}
	}
	if best < Floor {
		return 0
	}
	a, b := system[from:from+window], mic[from:from+window]

	// A millisecond grid first, then every sample around the winner: searching
	// every sample over a second of a ten-second window is forty times the work
	// for the same answer.
	// scan answers with how far the microphone trails the tap. A tap written
	// late is a microphone that appears to run early, so the answer comes back
	// negative and the offset is its opposite.
	coarse := scan(a, b, -Search, Search, Rate/1000)
	lag := -scan(a, b, coarse-Rate/1000, coarse+Rate/1000, 1)
	if lag <= 0 {
		return 0 // the tap is not late; nothing to do
	}
	return lag
}

// Search is how far apart the two channels may be before this gives up.
const Search = Rate / 2

// scan finds the lag at which b best matches a, by correlation.
func scan(a, b []float32, from, to, step int) int {
	bestLag, bestScore := 0, -2.0
	for lag := from; lag <= to; lag += step {
		var dot, na, nb float64
		for i := range a {
			j := i + lag
			if j < 0 || j >= len(b) {
				continue
			}
			dot += float64(a[i]) * float64(b[j])
			na += float64(a[i]) * float64(a[i])
			nb += float64(b[j]) * float64(b[j])
		}
		if na == 0 || nb == 0 {
			continue
		}
		if score := dot / (math.Sqrt(na) * math.Sqrt(nb)); score > bestScore {
			bestLag, bestScore = lag, score
		}
	}
	return bestLag
}

func sqrt(v float32) float32  { return float32(math.Sqrt(float64(v))) }
func clamp(v float32) float32 { return max(min(v, 1), -1) }

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
		binary.LittleEndian.PutUint16(body[2*i:], uint16(int16(clamp(s)*32767)))
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
// cached beside the recordings, anything else as it is. Cached because a
// browser asks for an hour-long file in dozens of ranges; rebuilt whenever the
// original is newer.
func Listenable(path, cache string) (string, error) {
	mic, system, ok := Sides(path)
	if !ok {
		return path, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return path, err
	}
	// The version is in the name so that changing how the fold sounds does not
	// leave everybody listening to copies made by the old one. A stale cache is
	// how a fixed echo goes on being heard.
	folded := filepath.Join(cache, Fold_+filepath.Base(path))

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

// Peaks is how many buckets a waveform is drawn with. Enough that a sentence is
// a visible bump on an hour-long meeting, few enough to be a row of divs.
const Peaks = 300

// Shape is the loudness of a recording across its length, normalised to 0..1.
//
// A seek bar with no waveform is a blind scrub: there is no way to see where
// the talking is, so finding the part you want means guessing and listening.
// One pass over the file the player is already being served.
func Shape(path string) []float32 {
	samples, err := Decode(path)
	if err != nil || len(samples) == 0 {
		return nil
	}
	bucket := max(len(samples)/Peaks, 1)
	out := make([]float32, 0, Peaks)
	loudest := float32(0)
	for at := 0; at < len(samples); at += bucket {
		v := float32(Loud(samples[at:min(at+bucket, len(samples))]))
		loudest = max(loudest, v)
		out = append(out, v)
	}
	if loudest > 0 {
		for i := range out {
			out[i] /= loudest
		}
	}
	return out
}
