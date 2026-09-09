package engine

import (
	"math"
	"slices"
)

// Preparing the audio before Whisper sees it, and judging what it gives back:
// the model is fixed, so the leverage is in the input and in what is let out.

// Quiet is the level audio is brought to before transcription, in dBFS. Real
// recordings here sit near -18; -14 lifts a mumbled word off the model's floor
// without approaching clipping.
const Quiet = -14.0

// Rumble is the high-pass corner. Below it is desk knocks, air conditioning and
// fan noise; the lowest male fundamental is around 85 Hz. Whisper's mel
// front-end starts at 0 Hz, so that energy reaches the model as if it meant
// something.
const Rumble = 80.0

// prepare cleans a recording before the model sees it: no offset, no rumble,
// and a level the model was trained around.
//
// Deliberately not a denoiser. Whisper is trained on noisy audio, and spectral
// subtraction leaves musical artefacts it reads as words. Invention over
// non-speech is handled by the VAD in front and the confidence check behind.
func prepare(samples []float32, rate int) []float32 {
	if len(samples) == 0 {
		return samples
	}
	out := make([]float32, len(samples))

	// One-pole high-pass, which takes the DC offset with it.
	rc := 1 / (2 * math.Pi * Rumble)
	a := float32(rc / (rc + 1/float64(rate)))
	var prevIn, prevOut float32
	for i, s := range samples {
		prevOut = a * (prevOut + s - prevIn)
		prevIn = s
		out[i] = prevOut
	}

	// To a level, not to full scale: peak normalisation rides on whatever door
	// slam was loudest.
	loud := active(out)
	if loud <= 0 {
		return out
	}
	gain := float32(math.Pow(10, Quiet/20) / loud)
	// Never quieter, and never so much louder that noise becomes a signal.
	gain = min(max(gain, 1), 8)
	if gain == 1 {
		return out
	}
	for i := range out {
		out[i] = clamp(out[i] * gain)
	}
	return out
}

// active is how loud the talking is, not how loud the file is: averaging in the
// silence between sentences makes a quiet meeting look quieter than it sounds.
func active(samples []float32) float64 {
	const window = 1600 // 100 ms
	var levels []float64
	for i := 0; i+window <= len(samples); i += window {
		var sum float64
		for _, s := range samples[i : i+window] {
			sum += float64(s) * float64(s)
		}
		levels = append(levels, math.Sqrt(sum/window))
	}
	if len(levels) == 0 {
		return 0
	}
	var loud []float64
	for _, l := range levels {
		if l > 1e-4 {
			loud = append(loud, l)
		}
	}
	if len(loud) == 0 {
		return 0
	}
	slices.Sort(loud)
	return loud[len(loud)*3/4]
}

func clamp(v float32) float32 { return max(min(v, 1), -1) }

// Doubtful is the mean token probability under which a row is invention rather
// than speech. Low on purpose: this catches inventions, it does not second-guess
// a difficult passage.
const Doubtful = 0.35

// Brief is how short a row must be before its confidence is worth doubting. A
// long stretch of poor audio is still somebody talking.
const Brief = 3

// believable rejects rows the model is not confident it heard — Whisper's habit
// of writing fluent text over silence, "Дякую за перегляд!" being the canonical
// one. Judged on the model's own numbers rather than a list of phrases, so it
// catches inventions nobody has seen yet.
func believable(tokens []float32) bool {
	if len(tokens) == 0 {
		return true // nothing to judge on; the VAD already had its say
	}
	var sum float64
	for _, p := range tokens {
		sum += float64(p)
	}
	mean := sum / float64(len(tokens))

	if mean < Doubtful {
		return false
	}
	// A short row is cheap to invent and cheap to lose, so it is held to a
	// higher bar than a paragraph.
	return len(tokens) > Brief || mean > Doubtful*1.6
}
