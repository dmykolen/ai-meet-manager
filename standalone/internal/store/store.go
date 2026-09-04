// Package store keeps everything the app remembers, in one SQLite file under
// ~/MeetingTranscriber.
//
// Four tables and no ORM. A meeting's summary is a JSON column rather than five
// more tables, because nothing ever queries inside it — it is read whole, shown
// whole, and rewritten whole. Turns are a real table because they are searched.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go: no second C toolchain for a database
)

// Kind is what a recording turned out to be.
type Kind string

const (
	Meeting Kind = "meeting" // somebody else was talking: diarized
	Note    Kind = "note"    // thinking aloud
)

// Status is where a recording is in the pipeline. Every one of these is shown
// to the person waiting, because a recording that stops moving has to say where.
type Status string

const (
	Queued       Status = "queued"
	Transcribing Status = "transcribing"
	Summarising  Status = "summarising"
	Done         Status = "done"
	Failed       Status = "failed"
)

// Recording is one meeting or note.
type Recording struct {
	ID       int64     `json:"id"`
	Kind     Kind      `json:"kind"`
	Title    string    `json:"title"` // from the summary; the file name until then
	Audio    string    `json:"audio"` // file name under recordings/, empty once deleted
	Started  time.Time `json:"started"`
	Duration float64   `json:"duration"`
	Language string    `json:"language"`
	Status   Status    `json:"status"`
	Progress float64   `json:"progress"`
	Problem  string    `json:"problem,omitempty"`
	Summary  *Summary  `json:"summary,omitempty"`
	Speakers []string  `json:"speakers,omitempty"`
	Turns    int       `json:"turns"`
	Note     string    `json:"note,omitempty"`
}

// Summary is stored as JSON; the shape belongs to the insights package, and this
// is deliberately a loose mirror so the two can move independently.
type Summary struct {
	Title         string    `json:"title"`
	Overview      string    `json:"overview"`
	Chapters      []Chapter `json:"chapters"`
	Topics        []string  `json:"topics"`
	Decisions     []string  `json:"decisions"`
	ActionItems   []Action  `json:"action_items"`
	OpenQuestions []string  `json:"open_questions"`
}

type Chapter struct {
	Start   float64 `json:"start"`
	Title   string  `json:"title"`
	Summary string  `json:"summary"`
}

type Action struct {
	Task  string `json:"task"`
	Owner string `json:"owner"`
	Due   string `json:"due"`
	Done  bool   `json:"done"`
}

// Turn is one row of a transcript.
type Turn struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker,omitempty"`
	Text    string  `json:"text"`
}

// DB is the handle. One per process.
type DB struct{ sql *sql.DB }

// Open creates the file and the schema if they are not there.
func Open(path string) (*DB, error) {
	// WAL so that the UI can read a transcript while a recording is being
	// written, which is the normal case and not an edge one.
	handle, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if _, err := handle.Exec(schema); err != nil {
		handle.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	// SQLite has no ADD COLUMN IF NOT EXISTS, and a database made before this
	// column existed will not get it from CREATE TABLE IF NOT EXISTS. Failing
	// here is the ordinary outcome — it means the column is already there.
	_, _ = handle.Exec(`ALTER TABLE recordings ADD COLUMN voices TEXT`)
	return &DB{sql: handle}, nil
}

func (d *DB) Close() error { return d.sql.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS recordings (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  kind      TEXT    NOT NULL,
  title     TEXT    NOT NULL DEFAULT '',
  audio     TEXT    NOT NULL,
  started   INTEGER NOT NULL,
  duration  REAL    NOT NULL DEFAULT 0,
  language  TEXT    NOT NULL DEFAULT '',
  status    TEXT    NOT NULL DEFAULT 'queued',
  progress  REAL    NOT NULL DEFAULT 0,
  problem   TEXT    NOT NULL DEFAULT '',
  summary   TEXT,
  note      TEXT    NOT NULL DEFAULT '',
  -- One voiceprint per speaker label, so that naming somebody after the fact
  -- still teaches the app what they sound like.
  voices    TEXT
);
CREATE INDEX IF NOT EXISTS recordings_started ON recordings(started DESC);

CREATE TABLE IF NOT EXISTS turns (
  recording INTEGER NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
  seq       INTEGER NOT NULL,
  start     REAL    NOT NULL,
  finish    REAL    NOT NULL,
  speaker   TEXT    NOT NULL DEFAULT '',
  text      TEXT    NOT NULL,
  PRIMARY KEY (recording, seq)
);

-- Full text over the transcript. Populated alongside turns rather than by a
-- trigger, so that a failed insert cannot leave the index describing rows that
-- are not there.
CREATE VIRTUAL TABLE IF NOT EXISTS transcript USING fts5(
  text, recording UNINDEXED, seq UNINDEXED, start UNINDEXED, speaker UNINDEXED,
  tokenize = 'unicode61 remove_diacritics 2'
);

-- Passages: the transcript in stretches worth finding, with the vector that
-- makes "what did we decide about access" find a passage that never says
-- "access". Without a key the vector is null and the same rows still serve
-- keyword search.
CREATE TABLE IF NOT EXISTS passages (
  recording INTEGER NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
  seq       INTEGER NOT NULL,
  start     REAL    NOT NULL,
  speaker   TEXT    NOT NULL DEFAULT '',
  text      TEXT    NOT NULL,
  vector    BLOB,
  PRIMARY KEY (recording, seq)
);

CREATE TABLE IF NOT EXISTS people (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT    NOT NULL UNIQUE,
  voiceprints TEXT    NOT NULL DEFAULT '[]'
);
`

// Add records a new recording and returns it with its id.
func (d *DB) Add(r Recording) (Recording, error) {
	res, err := d.sql.Exec(
		`INSERT INTO recordings (kind, title, audio, started, duration, status) VALUES (?, ?, ?, ?, ?, ?)`,
		r.Kind, r.Title, r.Audio, r.Started.Unix(), r.Duration, Queued)
	if err != nil {
		return r, err
	}
	r.ID, err = res.LastInsertId()
	r.Status = Queued
	return r, err
}

// Progress moves a recording along. Called often, so it touches one row and
// nothing else.
func (d *DB) Progress(id int64, status Status, fraction float64) error {
	_, err := d.sql.Exec(`UPDATE recordings SET status = ?, progress = ? WHERE id = ?`, status, fraction, id)
	return err
}

// Fail records why a recording stopped, which is the only thing worth showing
// when it did.
func (d *DB) Fail(id int64, cause error) error {
	_, err := d.sql.Exec(`UPDATE recordings SET status = ?, problem = ? WHERE id = ?`, Failed, cause.Error(), id)
	return err
}

// SaveTranscript writes the turns and the searchable copy in one transaction, so
// the two can never disagree.
func (d *DB) SaveTranscript(id int64, language string, duration float64, turns []Turn) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM turns WHERE recording = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM transcript WHERE recording = ?`, id); err != nil {
		return err
	}
	rows, err := tx.Prepare(`INSERT INTO turns (recording, seq, start, finish, speaker, text) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	index, err := tx.Prepare(`INSERT INTO transcript (text, recording, seq, start, speaker) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer index.Close()

	for i, t := range turns {
		if _, err := rows.Exec(id, i, t.Start, t.End, t.Speaker, t.Text); err != nil {
			return err
		}
		if _, err := index.Exec(t.Text, id, i, t.Start, t.Speaker); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE recordings SET language = ?, duration = ? WHERE id = ?`, language, duration, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveSummary stores the summary and takes the recording's title from it, which
// is what turns a list of file names into a list of subjects.
func (d *DB) SaveSummary(id int64, s *Summary) error {
	blob, err := json.Marshal(s)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(s.Title)
	if title == "" {
		_, err = d.sql.Exec(`UPDATE recordings SET summary = ? WHERE id = ?`, string(blob), id)
		return err
	}
	_, err = d.sql.Exec(`UPDATE recordings SET summary = ?, title = ? WHERE id = ?`, string(blob), title, id)
	return err
}

// SaveNote stores what the person typed against a recording.
func (d *DB) SaveNote(id int64, note string) error {
	_, err := d.sql.Exec(`UPDATE recordings SET note = ? WHERE id = ?`, note, id)
	return err
}

// Rename changes a speaker's label everywhere in one recording.
func (d *DB) Rename(id int64, from, to string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE turns SET speaker = ? WHERE recording = ? AND speaker = ?`, to, id, from); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE transcript SET speaker = ? WHERE recording = ? AND speaker = ?`, to, id, from); err != nil {
		return err
	}
	// The voiceprint moves with the label, so a second rename in the same
	// meeting still finds it.
	if _, err := tx.Exec(
		`UPDATE recordings SET voices = json_remove(json_set(voices, '$.' || ?, json_extract(voices, '$.' || ?)), '$.' || ?)
		 WHERE id = ? AND voices IS NOT NULL AND json_extract(voices, '$.' || ?) IS NOT NULL`,
		to, from, from, id, from); err != nil {
		return err
	}
	return tx.Commit()
}

// Dropped records that the audio of a recording has been deleted. The
// transcript, the summary and everything derived from them stay; only the sound
// is gone, and the interface has to be able to say so rather than offering a
// play button that does nothing.
func (d *DB) Dropped(id int64) error {
	_, err := d.sql.Exec(`UPDATE recordings SET audio = '' WHERE id = ?`, id)
	return err
}

// Audio is the file name of a recording, empty when the audio has been deleted.
func (d *DB) Audio(id int64) string {
	var name string
	_ = d.sql.QueryRow(`SELECT audio FROM recordings WHERE id = ?`, id).Scan(&name)
	return name
}

// Recent lists recordings, newest first, without their transcripts.
func (d *DB) Recent(limit int) ([]Recording, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.sql.Query(`
		SELECT r.id, r.kind, r.title, r.audio, r.started, r.duration, r.language,
		       r.status, r.progress, r.problem, r.summary, r.note,
		       (SELECT COUNT(*) FROM turns t WHERE t.recording = r.id)
		FROM recordings r ORDER BY r.started DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scan(rows)
}

// Get is one recording, with its speakers filled in.
func (d *DB) Get(id int64) (*Recording, error) {
	rows, err := d.sql.Query(`
		SELECT r.id, r.kind, r.title, r.audio, r.started, r.duration, r.language,
		       r.status, r.progress, r.problem, r.summary, r.note,
		       (SELECT COUNT(*) FROM turns t WHERE t.recording = r.id)
		FROM recordings r WHERE r.id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found, err := scan(rows)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, sql.ErrNoRows
	}
	r := found[0]
	if r.Speakers, err = d.speakers(id); err != nil {
		return nil, err
	}
	return &r, nil
}

// Turns is the transcript of one recording, in order.
func (d *DB) Turns(id int64) ([]Turn, error) {
	rows, err := d.sql.Query(`SELECT start, finish, speaker, text FROM turns WHERE recording = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []Turn
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.Start, &t.End, &t.Speaker, &t.Text); err != nil {
			return nil, err
		}
		turns = append(turns, t)
	}
	return turns, rows.Err()
}

func (d *DB) speakers(id int64) ([]string, error) {
	rows, err := d.sql.Query(`SELECT DISTINCT speaker FROM turns WHERE recording = ? AND speaker <> '' ORDER BY speaker`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var who []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		who = append(who, s)
	}
	return who, rows.Err()
}

// Delete forgets a recording and everything attached to it. The audio file is
// the caller's to remove: the database should not be deleting things it cannot
// put back.
func (d *DB) Delete(id int64) (string, error) {
	var audio string
	if err := d.sql.QueryRow(`SELECT audio FROM recordings WHERE id = ?`, id).Scan(&audio); err != nil {
		return "", err
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM transcript WHERE recording = ?`, id); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`DELETE FROM recordings WHERE id = ?`, id); err != nil {
		return "", err
	}
	return audio, tx.Commit()
}

func scan(rows *sql.Rows) ([]Recording, error) {
	var out []Recording
	for rows.Next() {
		var (
			r       Recording
			started int64
			summary sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.Kind, &r.Title, &r.Audio, &started, &r.Duration,
			&r.Language, &r.Status, &r.Progress, &r.Problem, &summary, &r.Note, &r.Turns); err != nil {
			return nil, err
		}
		r.Started = time.Unix(started, 0)
		if summary.Valid && summary.String != "" {
			var s Summary
			if err := json.Unmarshal([]byte(summary.String), &s); err == nil {
				r.Summary = &s
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ErrNotFound is what a missing recording looks like to a caller that should
// answer 404 rather than 500.
var ErrNotFound = errors.New("no such recording")

// TickAction marks one action item done or undone.
//
// Addressed by position rather than by an id of its own: the items live inside
// the summary, and the summary is rewritten whole whenever it is regenerated.
// A position survives that; a synthetic id would not.
func (d *DB) TickAction(id int64, index int, done bool) error {
	r, err := d.Get(id)
	if err != nil {
		return err
	}
	if r.Summary == nil || index < 0 || index >= len(r.Summary.ActionItems) {
		return fmt.Errorf("recording %d has no action item %d", id, index)
	}
	r.Summary.ActionItems[index].Done = done
	return d.SaveSummary(id, r.Summary)
}

// Outstanding is every action item from every meeting, newest meeting first.
// The inbox that answers "what did I commit to", which no single transcript can.
type Outstanding struct {
	Recording int64     `json:"recording"`
	Title     string    `json:"title"`
	Started   time.Time `json:"started"`
	Index     int       `json:"index"`
	Action
}

func (d *DB) Actions(includeDone bool) ([]Outstanding, error) {
	recent, err := d.Recent(500)
	if err != nil {
		return nil, err
	}
	out := []Outstanding{}
	for _, r := range recent {
		if r.Summary == nil {
			continue
		}
		for i, a := range r.Summary.ActionItems {
			if a.Done && !includeDone {
				continue
			}
			out = append(out, Outstanding{
				Recording: r.ID, Title: r.Title, Started: r.Started, Index: i, Action: a,
			})
		}
	}
	return out, nil
}
