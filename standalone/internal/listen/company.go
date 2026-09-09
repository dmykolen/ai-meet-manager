package listen

import (
	"math"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/audio"
)

// Company answers the question the system channel used to answer: is anybody
// else here?
//
// With the machine's own audio captured, that question is trivial — speech on
// the second channel is somebody talking to you. Turned off, the app has one
// microphone and has to work it out from the sound in the room, and there is
// exactly one honest signal in there: how many different voices it holds. A
// conversation has two. Thinking aloud has one, however long it goes on.
//
// So each utterance gets a voiceprint and is compared with the ones already
// heard. The moment one of them belongs to nobody seen so far, there are two
// people in the room and this is a meeting.
//
// What it cannot do is hear somebody who is not in the room. On headphones,
// with the system channel off, the far side reaches neither the microphone nor
// this, and the recording is a note. That is not a bug to be tuned away — the
// app genuinely cannot hear them, and pretending otherwise would mean guessing.
type Company struct {
	print  Voice
	voices [][]float32 // the centre of each voice heard
	held   []int       // how many utterances each has taken in
}

// Voice turns a stretch of speech into a voiceprint. Handed in rather than
// imported, so this package still knows nothing about the speaker models.
type Voice func([]float32) []float32

// Alike is the cosine above which two utterances are treated as one person.
//
// Measured across the whole library — 69 recordings whose answers the system
// channel already knew — and the honest summary is that this finds meetings
// well and clears monologues badly: at 0.65 it catches 33 of 36 meetings and
// leaves only 9 of 33 monologues alone. See exp/out/H-company.txt.
//
// So it is used for one thing only: labelling a recording a meeting, which
// decides whether it is worth a summary. It is never allowed to decide whether
// a recording is kept — at these numbers that would throw away one meeting in
// three, and the recorder does not discard anything when the system channel is
// off precisely because nothing here is good enough to.
const Alike = 0.65

// Enough is how much speech a voiceprint needs to mean anything. Under it the
// embedding is mostly room, and a room does not sound like a person. Five
// seconds rather than the second and a half tried first: it made no difference
// to how many meetings were found and a little to how many monologues survived.
const Enough = 5 * audio.SampleRate

func NewCompany(print Voice) *Company {
	return &Company{print: print}
}

// Heard takes one utterance and reports whether more than one person has now
// been heard. Once true it stays true: a meeting does not stop being one
// because somebody went quiet.
func (c *Company) Heard(samples []float32) bool {
	if c.print == nil || len(samples) < Enough {
		return len(c.voices) > 1
	}
	print := c.print(samples)
	if len(print) == 0 {
		return len(c.voices) > 1
	}

	// Against the centre of each voice, not against whichever utterance of it
	// arrived first. One person's utterances scatter around their own mean, so
	// comparing to a single sample makes the answer depend on which sample came
	// first — worth four monologues in the sweep.
	best, score := -1, Alike
	for i, known := range c.voices {
		if c := cosine(print, known); c >= score {
			best, score = i, c
		}
	}
	if best < 0 {
		c.voices = append(c.voices, append([]float32(nil), print...))
		c.held = append(c.held, 1)
		return len(c.voices) > 1
	}

	c.held[best]++
	n := float32(c.held[best])
	for j := range c.voices[best] {
		c.voices[best][j] += (print[j] - c.voices[best][j]) / n
	}
	return len(c.voices) > 1
}

// Alone forgets everybody, for the start of a new recording.
func (c *Company) Alone() { c.voices, c.held = c.voices[:0], c.held[:0] }

// Voices is how many distinct people have been heard.
func (c *Company) Voices() int { return len(c.voices) }

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}
