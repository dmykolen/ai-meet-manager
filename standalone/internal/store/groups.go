package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// A Group is a folder for recordings: a project, a team, a client.
//
// Manual, not clever. The app could try to guess which project a meeting
// belongs to, and would be wrong often enough that every card would need
// checking — which is more work than dragging it once.
type Group struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Colour string `json:"colour"` // empty means derived from the name
}

// Groups lists them with how many recordings each holds, busiest first.
func (d *DB) Groups() ([]Group, error) {
	rows, err := d.sql.Query(`
		SELECT g.id, g.name, g.colour, (
			SELECT COUNT(*) FROM recordings r
			WHERE r.folder = g.id AND r.deleted IS NULL
		) AS held
		FROM groups g ORDER BY held DESC, g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Colour, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Loose is how many recordings are in no project at all.
//
// The Library draws projects as a strip whose widths are these counts, and a
// strip that leaves out everything unfiled would show a library twice as tidy
// as it is.
func (d *DB) Loose() (int, error) {
	var n int
	err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM recordings WHERE folder IS NULL AND deleted IS NULL`).Scan(&n)
	return n, err
}

// NewGroup makes one, or returns the existing group of that name.
func (d *DB) NewGroup(name string) (Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Group{}, errors.New("a group needs a name")
	}
	if _, err := d.sql.Exec(
		`INSERT INTO groups (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
		return Group{}, err
	}
	var g Group
	g.Name = name
	err := d.sql.QueryRow(`SELECT id FROM groups WHERE name = ?`, name).Scan(&g.ID)
	return g, err
}

// Paint sets a project's colour, or clears it back to the one derived from its
// name when the value is empty.
func (d *DB) Paint(id int64, colour string) error {
	_, err := d.sql.Exec(`UPDATE groups SET colour = ? WHERE id = ?`, strings.TrimSpace(colour), id)
	return err
}

// RenameGroup changes a project's name. The derived colour follows the name, so
// a project renamed without an override changes colour — which is the right
// behaviour: it is a different project now.
func (d *DB) RenameGroup(id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a project needs a name")
	}
	_, err := d.sql.Exec(`UPDATE groups SET name = ? WHERE id = ?`, name, id)
	return err
}

// Assign files a recording under a group, or under none when group is zero.
func (d *DB) Assign(recording, group int64) error {
	if group == 0 {
		_, err := d.sql.Exec(`UPDATE recordings SET folder = NULL WHERE id = ?`, recording)
		return err
	}
	_, err := d.sql.Exec(`UPDATE recordings SET folder = ? WHERE id = ?`, group, recording)
	return err
}

// DropGroup removes a group. Its recordings stay, unfiled.
func (d *DB) DropGroup(id int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE recordings SET folder = NULL WHERE folder = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM groups WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Bin is the recordings that have been deleted but not yet thrown away.
//
// Deleting is one click and no dialog, which is only reasonable because it can
// be undone. Everything here still has its audio and its transcript; Empty is
// what actually destroys them.
func (d *DB) Bin() ([]Recording, error) { return d.list(`WHERE deleted IS NOT NULL`, -1) }

// Bury moves a recording to the bin.
func (d *DB) Bury(id int64) error {
	_, err := d.sql.Exec(`UPDATE recordings SET deleted = ? WHERE id = ?`, time.Now().Unix(), id)
	return err
}

// Restore takes it back out.
func (d *DB) Restore(id int64) error {
	_, err := d.sql.Exec(`UPDATE recordings SET deleted = NULL WHERE id = ?`, id)
	return err
}

// Buried lists what is in the bin and older than the given age, which is what
// the daily sweep empties. A zero age means everything in it.
func (d *DB) Buried(olderThan time.Duration) ([]Recording, error) {
	cutoff := time.Now().Add(-olderThan).Unix()
	rows, err := d.sql.Query(
		`SELECT id, audio FROM recordings WHERE deleted IS NOT NULL AND deleted <= ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Recording
	for rows.Next() {
		var r Recording
		var audio sql.NullString
		if err := rows.Scan(&r.ID, &audio); err != nil {
			return nil, err
		}
		r.Audio = audio.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// Skipped records that the listener threw a recording away. Nothing about the
// recording is kept — only that it happened, so the app can show that the
// setting is doing something rather than asking to be believed.
func (d *DB) Skipped(seconds float64, why string) error {
	_, err := d.sql.Exec(`INSERT INTO discarded (at, seconds, why) VALUES (?, ?, ?)`,
		time.Now().Unix(), seconds, why)
	return err
}

// Saved is what was thrown away since a moment: how many, and how long they
// were altogether.
func (d *DB) Saved(since time.Time) (count int, seconds float64) {
	_ = d.sql.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(seconds), 0) FROM discarded WHERE at >= ?`,
		since.Unix()).Scan(&count, &seconds)
	return count, seconds
}

// A Mark is one recording as the timeline draws it: enough to place a tick and
// colour it, and nothing else.
//
// The timeline shows a year at a time. Reusing list() there would send every
// transcript, summary and note for four hundred recordings to draw four hundred
// three-pixel marks.
type Mark struct {
	ID       int64     `json:"id"`
	Kind     Kind      `json:"kind"`
	Started  time.Time `json:"started"`
	Duration float64   `json:"duration"`
	Folder   int64     `json:"folder"`
}

// Span is every recording between two moments, oldest first.
func (d *DB) Span(from, to time.Time) ([]Mark, error) {
	rows, err := d.sql.Query(`
		SELECT id, kind, started, duration, COALESCE(folder, 0)
		FROM recordings
		WHERE deleted IS NULL AND started >= ? AND started <= ?
		ORDER BY started`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Mark{}
	for rows.Next() {
		var m Mark
		var started int64
		if err := rows.Scan(&m.ID, &m.Kind, &started, &m.Duration, &m.Folder); err != nil {
			return nil, err
		}
		m.Started = time.Unix(started, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}
