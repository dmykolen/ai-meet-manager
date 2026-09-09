package store

import (
	"sort"
	"strings"
	"time"
)

// Standing is where a project stands, gathered from the meetings in it.
//
// This is the mechanical version of what PROJECTS.md describes: no model, no
// judgement, only what the summaries already say, folded together so that the
// same commitment made in three meetings is one line saying it was made three
// times rather than three lines pretending to be different work.
//
// It is deliberately useful on its own. A project page that needs an API key to
// show anything at all would be a page most days show nothing.
type Standing struct {
	Meetings  int       `json:"meetings"`
	Hours     float64   `json:"hours"`
	First     time.Time `json:"first"`
	Last      time.Time `json:"last"`
	Work      []Thread  `json:"work"`
	Decisions []Thread  `json:"decisions"`
	Questions []Thread  `json:"questions"`
	People    []Face    `json:"people"`
	// One paragraph on where the project stands, and whether a model wrote it
	// rather than the mechanical fold below.
	Status  string `json:"status"`
	Written bool   `json:"written"`
	// How many of these meetings the model has folded in. During a rebuild it
	// climbs, which is the only honest progress bar available: the document
	// itself counts what it has read.
	Folded int `json:"folded"`
}

// A Thread is one thing the project's meetings keep saying: the text, how many
// meetings said it, and where it was said last. Owner and Due are empty for
// anything that is not a commitment.
type Thread struct {
	// The line's id in the kept document, or zero when this came from the fold.
	Item   int       `json:"item"`
	State  string    `json:"state"`
	By     string    `json:"by"`
	Pinned bool      `json:"pinned"`
	Text   string    `json:"text"`
	Owner  string    `json:"owner"`
	Due    string    `json:"due"`
	Done   bool      `json:"done"`
	Times  int       `json:"times"`
	From   int64     `json:"from"`  // the meeting that said it last
	Index  int       `json:"index"` // its place in that meeting's action items
	When   time.Time `json:"when"`
}

// A Face is somebody heard in the project, ever.
type Face struct {
	Name     string    `json:"name"`
	Seconds  float64   `json:"seconds"`
	Meetings int       `json:"meetings"`
	Last     time.Time `json:"last"`
}

// Standing gathers a project. Recordings still being transcribed contribute
// their people but not their summaries, which do not exist yet.
func (d *DB) Standing(group int64) (*Standing, error) {
	rows, err := d.list(`WHERE r.folder = ? AND r.deleted IS NULL`, 1000, group)
	if err != nil {
		return nil, err
	}
	out := &Standing{Work: []Thread{}, Decisions: []Thread{}, Questions: []Thread{}, People: []Face{}}
	work, decided, asked := folder{}, folder{}, folder{}

	for _, r := range rows {
		out.Meetings++
		out.Hours += r.Duration / 3600
		if out.First.IsZero() || r.Started.Before(out.First) {
			out.First = r.Started
		}
		if r.Started.After(out.Last) {
			out.Last = r.Started
		}
		if r.Summary == nil {
			continue
		}
		for i, a := range r.Summary.ActionItems {
			work.add(a.Task, r, i, a)
		}
		for _, t := range r.Summary.Decisions {
			decided.add(t, r, -1, Action{})
		}
		for _, t := range r.Summary.OpenQuestions {
			asked.add(t, r, -1, Action{})
		}
	}

	// Open work first, and within that the thing that has been promised most
	// often — which is the honest signal that nobody is doing it.
	out.Work = work.sorted(func(a, b Thread) bool {
		if a.Done != b.Done {
			return !a.Done
		}
		if a.Times != b.Times {
			return a.Times > b.Times
		}
		return a.When.After(b.When)
	})
	out.Decisions = decided.sorted(func(a, b Thread) bool { return a.When.After(b.When) })
	out.Questions = asked.sorted(func(a, b Thread) bool {
		if a.Times != b.Times {
			return a.Times > b.Times
		}
		return a.When.After(b.When)
	})

	out.People, err = d.faces(group)
	if err != nil {
		return nil, err
	}

	// Once the model has kept a document for this project, that is the answer:
	// it deduplicates by talking about ids rather than by matching text, it
	// carries decisions that were overturned, and it holds a paragraph saying
	// where things stand. The fold above remains the answer for a project the
	// model has never seen, and for anybody with no key at all.
	kept, err := d.Held(group)
	if err != nil || kept == nil {
		return out, nil
	}
	out.Status = kept.Status
	out.Written = true
	out.Folded = len(kept.Seen)
	out.Work = threads(kept.Work)
	out.Decisions = threads(kept.Decisions)
	out.Questions = threads(kept.Questions)
	return out, nil
}

// threads is the stored document in the shape the page already draws.
func threads(items []Item) []Thread {
	out := make([]Thread, 0, len(items))
	for _, it := range items {
		out = append(out, Thread{
			Item: it.ID, Text: it.Text, Owner: it.Owner, Due: it.Due,
			Done:  it.State == "done" || it.State == "answered" || it.State == "overturned",
			State: it.State, By: it.By, Pinned: it.Pinned,
			Times: it.Times, From: it.From, Index: -1, When: it.When,
		})
	}
	return out
}

// folder collapses repeats. The key is the text with case and punctuation
// removed, so "Узгодити ролі." and "узгодити ролі" are one commitment — which
// is as far as this can go without a model, and further than showing both.
type folder map[string]*Thread

func (f folder) add(text string, r Recording, index int, a Action) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	k := strings.Map(func(c rune) rune {
		if strings.ContainsRune(".,;:!?—-–'\"()", c) {
			return -1
		}
		return c
	}, strings.ToLower(strings.Join(strings.Fields(text), " ")))

	held, seen := f[k]
	if !seen {
		held = &Thread{Text: text}
		f[k] = held
	}
	held.Times++
	// The most recent mention wins: an owner or a due date named later is the
	// one that stands, and a thing ticked off anywhere is done.
	if r.Started.After(held.When) {
		held.Text, held.When, held.From, held.Index = text, r.Started, r.ID, index
		held.Owner, held.Due = a.Owner, a.Due
	}
	held.Done = held.Done || a.Done
}

func (f folder) sorted(less func(a, b Thread) bool) []Thread {
	out := make([]Thread, 0, len(f))
	for _, s := range f {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// faces is everybody ever heard in a project, longest-heard first.
func (d *DB) faces(group int64) ([]Face, error) {
	rows, err := d.sql.Query(`
		SELECT t.speaker, SUM(t.finish - t.start), COUNT(DISTINCT t.recording), MAX(r.started)
		FROM turns t JOIN recordings r ON r.id = t.recording
		WHERE r.folder = ? AND r.deleted IS NULL AND t.speaker <> ''
		GROUP BY t.speaker ORDER BY SUM(t.finish - t.start) DESC`, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Face{}
	for rows.Next() {
		var f Face
		var last int64
		if err := rows.Scan(&f.Name, &f.Seconds, &f.Meetings, &last); err != nil {
			return nil, err
		}
		f.Last = time.Unix(last, 0)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Moment finds where in a recording something was said, so a line of the
// project document can open the meeting at the second rather than at the top.
//
// The document stores which meeting a line came from but not the moment inside
// it — a summary's commitments carry no timestamps. The words are enough: the
// transcript is already indexed, so the turn that best matches the line is one
// query away. Nothing is invented; if no turn matches, the meeting opens at the
// beginning, which is what it did before.
func (d *DB) Moment(recording int64, text string) float64 {
	words := strings.Fields(strings.Map(func(r rune) rune {
		if strings.ContainsRune(`"'*()[]{}^:-`, r) {
			return ' ' // FTS5 operators; a commitment is a phrase, not a query
		}
		return r
	}, text))
	if len(words) == 0 {
		return 0
	}
	if len(words) > 12 {
		words = words[:12]
	}

	var at float64
	// OR rather than a phrase: the summary rewords what was said, so the turn
	// that shares the most of these words is the one wanted.
	_ = d.sql.QueryRow(`
		SELECT start FROM transcript
		WHERE recording = ? AND transcript MATCH ?
		ORDER BY rank LIMIT 1`, recording, strings.Join(words, " OR ")).Scan(&at)
	return at
}
