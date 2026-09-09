package store

import (
	"cmp"
	"encoding/json"
	"time"
)

// Kept is a project's state as the model maintains it: one living document that
// every meeting updates, rather than a pile of summaries.
//
// The point of storing it, rather than folding the summaries together on every
// read, is that the model answers with *operations against these ids*. It sees
// what is already here before it says anything, so the same commitment made in
// three meetings comes back as "that is number 14 again" instead of as a
// fourteenth, fifteenth and sixteenth line. No similarity threshold anywhere.
type Kept struct {
	Status    string    `json:"status"`
	Work      []Item    `json:"work"`
	Decisions []Item    `json:"decisions"`
	Questions []Item    `json:"questions"`
	Seen      []int64   `json:"seen"` // recordings already folded in
	Next      int       `json:"next"` // the next id to hand out; never reused
	Updated   time.Time `json:"updated"`
}

// An Item is one line of the document, and where it came from.
//
// Provenance is not decoration. Without it the page is a claim nobody can
// check, and the first time it is wrong about something it stops being trusted
// entirely. With it, every line is one click from the moment it was said.
type Item struct {
	ID    int    `json:"id"`
	Text  string `json:"text"`
	Owner string `json:"owner"`
	Due   string `json:"due"`
	// open, done, dropped for work; standing, overturned for decisions;
	// open, answered for questions.
	State string `json:"state"`
	// How many meetings have said it. A commitment restated three times is one
	// nobody is doing, and that is visible without any extra machinery.
	Times int       `json:"times"`
	From  int64     `json:"from"` // the meeting that last touched it
	When  time.Time `json:"when"`
	// Set once a person has edited the text. The model may close a pinned item
	// or mark it restated; it may never reword it. Anything else and the app
	// quietly undoes somebody's work.
	Pinned bool `json:"pinned"`
	// For a decision that was overturned: what replaced it.
	By string `json:"by"`
}

// Held reads a project's document, or nil when the model has never run on it.
func (d *DB) Held(group int64) (*Kept, error) {
	var raw string
	if err := d.sql.QueryRow(`SELECT state FROM groups WHERE id = ?`, group).Scan(&raw); err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var kept Kept
	if err := json.Unmarshal([]byte(raw), &kept); err != nil {
		return nil, nil // a corrupt document is a document to rebuild, not an error to show
	}
	return &kept, nil
}

// Keep writes it back.
func (d *DB) Keep(group int64, kept *Kept) error {
	kept.Updated = time.Now()
	blob, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`UPDATE groups SET state = ? WHERE id = ?`, string(blob), group)
	return err
}

// Forget the document, so the next pass builds it again from the first meeting.
func (d *DB) Rebuild(group int64) error {
	_, err := d.sql.Exec(`UPDATE groups SET state = '' WHERE id = ?`, group)
	return err
}

// Word is one change the model asks for. It names an existing line by id or
// adds a new one; it never hands back a fresh list.
type Word struct {
	Do    string `json:"do"`   // add, update, close, restate, answer, overturn
	Kind  string `json:"kind"` // work, decision, question
	ID    int    `json:"id"`
	Text  string `json:"text"`
	Owner string `json:"owner"`
	Due   string `json:"due"`
	State string `json:"state"`
}

// Apply folds one meeting's worth of operations into the document.
//
// Everything the model can get wrong is bounded here rather than trusted: it
// cannot invent an id, cannot reword something a person edited, and cannot
// remove a line at all.
func (k *Kept) Apply(words []Word, status string, from int64, when time.Time) {
	if status != "" {
		k.Status = status
	}
	for _, w := range words {
		list := k.list(w.Kind)
		if list == nil {
			continue
		}
		if w.Do == "add" {
			if w.Text == "" {
				continue
			}
			k.Next++
			*list = append(*list, Item{
				ID: k.Next, Text: w.Text, Owner: w.Owner, Due: w.Due,
				State: firstState(w.Kind), Times: 1, From: from, When: when,
			})
			continue
		}
		at := -1
		for i := range *list {
			if (*list)[i].ID == w.ID {
				at = i
			}
		}
		if at < 0 {
			continue // an id it made up
		}
		it := &(*list)[at]
		it.From, it.When = from, when
		switch w.Do {
		case "restate":
			it.Times++
		case "update":
			it.Times++
			if !it.Pinned {
				if w.Text != "" {
					it.Text = w.Text
				}
				it.Owner, it.Due = w.Owner, w.Due
			}
		case "close":
			it.State = cmp.Or(w.State, "done")
		case "answer":
			it.State = "answered"
		case "overturn":
			it.State, it.By = "overturned", w.Text
		}
	}
	k.Seen = append(k.Seen, from)
}

func (k *Kept) list(kind string) *[]Item {
	switch kind {
	case "work":
		return &k.Work
	case "decision":
		return &k.Decisions
	case "question":
		return &k.Questions
	}
	return nil
}

func firstState(kind string) string {
	if kind == "decision" {
		return "standing"
	}
	return "open"
}

// Pin is a person editing a line. From then on the model may close it or note
// that it was said again, but never rewrite it.
func (d *DB) Pin(group int64, id int, text, owner, due string) error {
	kept, err := d.Held(group)
	if err != nil || kept == nil {
		return err
	}
	for _, list := range []*[]Item{&kept.Work, &kept.Decisions, &kept.Questions} {
		for i := range *list {
			if (*list)[i].ID != id {
				continue
			}
			it := &(*list)[i]
			it.Pinned = true
			if text != "" {
				it.Text = text
			}
			it.Owner, it.Due = owner, due
			return d.Keep(group, kept)
		}
	}
	return nil
}

// Tick ticks a line off, or puts it back.
func (d *DB) Tick(group int64, id int, done bool) error {
	kept, err := d.Held(group)
	if err != nil || kept == nil {
		return err
	}
	for i := range kept.Work {
		if kept.Work[i].ID == id {
			kept.Work[i].State = map[bool]string{true: "done", false: "open"}[done]
			return d.Keep(group, kept)
		}
	}
	return nil
}
