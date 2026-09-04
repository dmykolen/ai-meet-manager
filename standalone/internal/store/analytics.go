package store

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Analytics is what the shape of a meeting looks like from outside the words:
// who held the floor, for how long, how fast, and who was actually asking
// things rather than telling.
//
// Computed from the stored rows every time it is asked for rather than saved.
// It is a walk over a few hundred turns, it can never go stale, and it follows
// a speaker being renamed without a migration.
type Analytics struct {
	Speech   float64  `json:"speech"`   // seconds anybody was talking
	Silence  float64  `json:"silence"`  // seconds nobody was
	Overlap  float64  `json:"overlap"`  // seconds two people were, at once
	Words    int      `json:"words"`    // in the whole meeting
	Pace     float64  `json:"pace"`     // words per minute while somebody was talking
	Speakers []Voice  `json:"speakers"` // most talkative first
	Balance  float64  `json:"balance"`  // 0 = one person spoke, 1 = everyone equally
	Busiest  []Moment `json:"busiest"`  // the densest stretches, for the shape of the hour
}

// A Voice is one person's share of a meeting.
type Voice struct {
	Speaker   string  `json:"speaker"`
	Seconds   float64 `json:"seconds"`
	Share     float64 `json:"share"`
	Turns     int     `json:"turns"`
	Longest   float64 `json:"longest"`   // their longest single stretch
	Words     int     `json:"words"`     //
	Pace      float64 `json:"pace"`      // their own words per minute
	Questions int     `json:"questions"` // turns of theirs that asked something
}

// A Moment is one slice of the timeline and how much was said in it, so the
// meeting can be drawn as a shape rather than a list.
type Moment struct {
	At    float64 `json:"at"`
	Words int     `json:"words"`
}

// Moments is how many slices the timeline is cut into. Enough to show where the
// meeting was dense and where it drifted, few enough to draw as bars in a card.
const Moments = 48

// Analyse measures a transcript. duration is the length of the recording, which
// is what makes silence meaningful — without it there is nothing to subtract
// the talking from.
func Analyse(turns []Turn, duration float64) Analytics {
	if len(turns) == 0 {
		return Analytics{Silence: duration}
	}

	a := Analytics{Silence: duration}
	each := map[string]*Voice{}
	var order []string

	for _, t := range turns {
		length := max(t.End-t.Start, 0)
		words := len(strings.Fields(t.Text))
		a.Words += words

		who := t.Speaker
		if who == "" {
			who = "Speaker"
		}
		v, seen := each[who]
		if !seen {
			v = &Voice{Speaker: who}
			each[who], order = v, append(order, who)
		}
		v.Seconds += length
		v.Turns++
		v.Words += words
		v.Longest = max(v.Longest, length)
		if asksSomething(t.Text) {
			v.Questions++
		}
	}

	// Speech is the union of the rows, not their sum: two people talking at
	// once is one second of meeting, and the difference between the two is
	// exactly how much they talked over each other.
	spoken := 0.0
	for _, v := range each {
		spoken += v.Seconds
	}
	a.Speech = union(turns)
	a.Silence = max(duration-a.Speech, 0)
	a.Overlap = max(spoken-a.Speech, 0)
	if a.Speech > 0 {
		a.Pace = float64(a.Words) / (a.Speech / 60)
	}

	for _, who := range order {
		v := each[who]
		if spoken > 0 {
			v.Share = v.Seconds / spoken
		}
		if v.Seconds > 0 {
			v.Pace = float64(v.Words) / (v.Seconds / 60)
		}
		a.Speakers = append(a.Speakers, *v)
	}
	sort.Slice(a.Speakers, func(i, j int) bool { return a.Speakers[i].Seconds > a.Speakers[j].Seconds })
	a.Balance = balance(a.Speakers)
	a.Busiest = shape(turns, duration)
	return a
}

// union is the total time covered by the rows, counting an overlap once.
func union(turns []Turn) float64 {
	spans := make([][2]float64, 0, len(turns))
	for _, t := range turns {
		if t.End > t.Start {
			spans = append(spans, [2]float64{t.Start, t.End})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })

	total, from, to := 0.0, 0.0, -1.0
	for _, s := range spans {
		if s[0] > to {
			if to > from {
				total += to - from
			}
			from, to = s[0], s[1]
			continue
		}
		to = max(to, s[1])
	}
	if to > from {
		total += to - from
	}
	return total
}

// balance is how evenly the floor was shared, as normalised entropy: 1 when
// everybody spoke the same amount, 0 when one person spoke and nobody else did.
//
// A single number rather than a table, because the useful question is "was this
// a discussion or a broadcast" and the table is right underneath it anyway.
func balance(voices []Voice) float64 {
	if len(voices) < 2 {
		return 0
	}
	h := 0.0
	for _, v := range voices {
		if v.Share > 0 {
			h -= v.Share * math.Log(v.Share)
		}
	}
	return h / math.Log(float64(len(voices)))
}

// shape buckets the words across the timeline, so a meeting can be drawn.
func shape(turns []Turn, duration float64) []Moment {
	if duration <= 0 {
		return nil
	}
	slice := duration / Moments
	out := make([]Moment, Moments)
	for i := range out {
		out[i].At = float64(i) * slice
	}
	for _, t := range turns {
		at := min(int(t.Start/slice), Moments-1)
		if at >= 0 {
			out[at].Words += len(strings.Fields(t.Text))
		}
	}
	return out
}

// asksSomething is deliberately crude: a question mark, or an opening that
// almost always is one. It exists to say "Olena asked most of the questions",
// which is true often enough to be worth showing and cheap enough not to need a
// model. Ukrainian and English, since those are what this app hears.
func asksSomething(text string) bool {
	if strings.Contains(text, "?") {
		return true
	}
	first := strings.ToLower(strings.TrimLeftFunc(text, func(r rune) bool { return !unicode.IsLetter(r) }))
	for _, opener := range []string{
		"чи ", "хто ", "що ", "коли ", "чому ", "як ", "де ", "скільки ", "який ", "яка ", "яке ", "які ",
		"what ", "why ", "how ", "when ", "who ", "where ", "which ", "can we ", "should we ", "do we ",
	} {
		if strings.HasPrefix(first, opener) {
			return true
		}
	}
	return false
}
