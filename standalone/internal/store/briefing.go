package store

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

// Briefing is the answer to the two questions somebody actually opens this app
// with: what did I miss, and what did I say I would do.
//
// Everything here is read out of summaries that were already written. No model
// is called, nothing is sent anywhere, and it is the same few hundred rows the
// Library screen reads — so it is instant and it works with no key.
type Briefing struct {
	Since    time.Time     `json:"since"`
	Meetings []Recording   `json:"meetings"` // in the window, newest first
	Minutes  int           `json:"minutes"`  // spent in them
	Decided  []Said        `json:"decided"`  // what was settled, with where
	Mine     []Outstanding `json:"mine"`     // still open, oldest first
	Overdue  []Outstanding `json:"overdue"`  // open, and their deadline has passed
	Nagging  []Nagging     `json:"nagging"`  // asked in more than one meeting and still open
	Voices   []string      `json:"voices"`   // who was in them
}

// Said is one line from a summary, and the meeting it came from.
type Said struct {
	Recording int64     `json:"recording"`
	Title     string    `json:"title"`
	Started   time.Time `json:"started"`
	Text      string    `json:"text"`
}

// Nagging is a question that keeps coming back.
//
// This is the thing a pile of transcripts knows and a person does not: the same
// thing was left unresolved in three meetings, and nobody noticed because each
// meeting only remembers itself.
type Nagging struct {
	Text  string `json:"text"`
	Times int    `json:"times"`
	Said  []Said `json:"said"`
}

// Brief looks back over a window — a day for a morning check, a week for a
// Friday one.
func (d *DB) Brief(days int) (*Briefing, error) {
	if days <= 0 {
		days = 1
	}
	recent, err := d.Recent(500)
	if err != nil {
		return nil, err
	}
	since := time.Now().AddDate(0, 0, -days)
	b := &Briefing{
		Since: since, Meetings: []Recording{}, Decided: []Said{},
		Mine: []Outstanding{}, Overdue: []Outstanding{}, Nagging: []Nagging{}, Voices: []string{},
	}

	seconds, heard := 0.0, map[string]bool{}
	asked := map[string][]Said{}

	for _, r := range recent {
		inWindow := r.Started.After(since)
		if inWindow {
			b.Meetings = append(b.Meetings, r)
			seconds += r.Duration
			for _, who := range r.Speakers {
				if !strings.HasPrefix(who, "SPEAKER_") {
					heard[who] = true
				}
			}
		}
		if r.Summary == nil {
			continue
		}
		where := Said{Recording: r.ID, Title: r.Title, Started: r.Started}

		if inWindow {
			for _, decision := range r.Summary.Decisions {
				line := where
				line.Text = decision
				b.Decided = append(b.Decided, line)
			}
		}
		// Open questions are gathered from everything, not just the window: a
		// question asked a month ago and again yesterday is exactly the case
		// worth surfacing.
		for _, q := range r.Summary.OpenQuestions {
			line := where
			line.Text = q
			key := fingerprint(q)
			asked[key] = append(asked[key], line)
		}
		// Commitments too — an overdue item does not become less overdue for
		// being older than the window.
		for i, a := range r.Summary.ActionItems {
			if a.Done {
				continue
			}
			item := Outstanding{Recording: r.ID, Title: r.Title, Started: r.Started, Index: i, Action: a}
			b.Mine = append(b.Mine, item)
			if overdue(a.Due, r.Started) {
				b.Overdue = append(b.Overdue, item)
			}
		}
	}

	b.Minutes = int(seconds / 60)
	for who := range heard {
		b.Voices = append(b.Voices, who)
	}
	sort.Strings(b.Voices)

	for _, said := range asked {
		if len(said) < 2 {
			continue
		}
		sort.Slice(said, func(i, j int) bool { return said[i].Started.After(said[j].Started) })
		b.Nagging = append(b.Nagging, Nagging{Text: said[0].Text, Times: len(said), Said: said})
	}
	sort.Slice(b.Nagging, func(i, j int) bool { return b.Nagging[i].Times > b.Nagging[j].Times })

	sort.Slice(b.Mine, func(i, j int) bool { return b.Mine[i].Started.Before(b.Mine[j].Started) })
	sort.Slice(b.Overdue, func(i, j int) bool { return b.Overdue[i].Started.Before(b.Overdue[j].Started) })
	return b, nil
}

// fingerprint reduces a sentence to the words that carry it, so that "Which IP
// ranges do we need to allow?" and "What IP ranges should be allowed?" land in
// the same bucket.
//
// Crude on purpose. The alternative is embedding every open question of every
// meeting, which costs a key, a network round trip and a vector index to catch
// a handful more — and being wrong here shows a person two lines instead of
// one, which is a small thing to be wrong about.
func fingerprint(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	kept := make([]string, 0, len(words))
	for _, w := range words {
		if len([]rune(w)) > 3 && !filler[w] {
			kept = append(kept, w)
		}
	}
	sort.Strings(kept)
	if len(kept) > 6 {
		kept = kept[:6]
	}
	return strings.Join(kept, " ")
}

// filler is the words that say nothing about which question this is. Ukrainian
// and English, since those are what this app hears.
var filler = map[string]bool{
	"який": true, "яка": true, "яке": true, "які": true, "треба": true, "потрібно": true,
	"можна": true, "буде": true, "було": true, "щодо": true, "цього": true, "цьому": true,
	"what": true, "which": true, "should": true, "would": true, "could": true, "need": true,
	"about": true, "this": true, "that": true, "there": true, "does": true, "with": true,
	"from": true, "have": true, "will": true, "when": true, "были": true,
}

// overdue reads the deadline the way a person wrote it. Most are relative —
// "сьогодні", "next week", "до п'ятниці" — so the meeting's own date is the
// clock they were said against.
//
// Only the unambiguous ones count. A deadline this cannot read is left alone
// rather than guessed at, because a false "overdue" beside somebody's name is
// worse than a missing one.
func overdue(due string, said time.Time) bool {
	d := strings.ToLower(strings.TrimSpace(due))
	if d == "" {
		return false
	}
	day := 24 * time.Hour
	for phrase, within := range map[string]time.Duration{
		"сьогодні": 0, "today": 0, "зараз": 0, "now": 0, "asap": 0,
		"завтра": day, "tomorrow": day,
		"цього тижня": 7 * day, "this week": 7 * day, "на тижні": 7 * day,
		"next week": 14 * day, "наступного тижня": 14 * day,
	} {
		if strings.Contains(d, phrase) {
			return time.Since(said.Add(within)) > day
		}
	}
	// A written date, if it is one we can read.
	for _, layout := range []string{"2006-01-02", "02.01.2006", "02.01.06", "01/02/2006"} {
		if when, err := time.Parse(layout, strings.TrimSpace(due)); err == nil {
			return time.Now().After(when.Add(day))
		}
	}
	return false
}
