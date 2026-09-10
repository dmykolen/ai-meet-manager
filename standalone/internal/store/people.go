package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
)

// Keep is how many voiceprints one person may accumulate.
//
// Not one: a headset and a laptop microphone are measurably different voices.
// Not unbounded: matching takes the *best* sample, so every extra one can only
// make a false positive likelier. The cost is not arithmetic — fifty people at
// ten samples is under a millisecond.
const Keep = 10

// Match is how close two voiceprints have to be to be called the same person.
//
// Higher than the 0.9 the diarizer clusters at, because the cost of being wrong
// is different: mislabelling two speakers inside one meeting is visible and
// correctable, but putting a colleague's name on a stranger's words in the
// Library is worse than leaving them as SPEAKER_01.
const Match = 0.55

// Rejoin is how alike two clusters of one recording have to be before they are
// called the same person and put back together.
//
// Well above Match, and for a different reason: Match asks "could this be
// Olena", which several people in a room can answer yes to, while this asks
// "are these two the same voice". Two recordings of one person sit around 0.7
// and two different people near 0, so this sits just under the first.
const Rejoin = 0.75

// A Person is somebody the app has been told the name of, and the voiceprints
// it has collected for them since.
type Person struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	Voiceprints [][]float32 `json:"-"`
	Samples     int         `json:"samples"`
	Meetings    int         `json:"meetings"`
	Colour      string      `json:"colour"` // empty means derived from the name
	Sources     []Source    `json:"sources"`
}

// A Source is where one voiceprint was taken from. Without it a saved sample is
// a vector nobody can examine; with it, "this is what I think Olena sounds
// like" is one click from being played and disagreed with.
type Source struct {
	Recording int64   `json:"recording"`
	Speaker   string  `json:"speaker"`
	Title     string  `json:"title"`
	Audio     string  `json:"audio"`
	Start     float64 `json:"start"`
	Finish    float64 `json:"finish"`
}

// People is everybody enrolled, most heard first.
func (d *DB) People() ([]Person, error) {
	rows, err := d.sql.Query(`
		SELECT p.id, p.name, p.voiceprints, p.colour, p.sources,
		       (SELECT COUNT(DISTINCT recording) FROM turns WHERE speaker = p.name)
		FROM people p ORDER BY p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var people []Person
	for rows.Next() {
		var p Person
		var raw, sources string
		if err := rows.Scan(&p.ID, &p.Name, &raw, &p.Colour, &sources, &p.Meetings); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &p.Voiceprints); err != nil {
			// A corrupt row should cost one person's recognition, not the list.
			p.Voiceprints = nil
		}
		_ = json.Unmarshal([]byte(sources), &p.Sources)
		p.Samples = len(p.Voiceprints)
		people = append(people, p)
	}
	return people, rows.Err()
}

// Remember files another sample of a voice under a name, so that recognition
// improves every time somebody is named rather than staying as good as the
// first thirty seconds it ever heard.
//
// The source travels with the sample. A voiceprint on its own is 512 numbers
// nobody can check; with the meeting and the moment it came from, the claim
// "this is Olena" can be played back and disagreed with.
func (d *DB) Remember(name string, print []float32, from Source) error {
	if name == "" || len(print) == 0 {
		return errors.New("nothing to remember")
	}
	var raw, sources string
	err := d.sql.QueryRow(`SELECT voiceprints, sources FROM people WHERE name = ?`, name).
		Scan(&raw, &sources)

	var kept [][]float32
	var came []Source
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		_ = json.Unmarshal([]byte(raw), &kept)
		_ = json.Unmarshal([]byte(sources), &came)
	}
	kept = append(kept, print)
	// An older row may hold fewer sources than prints; pad so the two arrays
	// stay index-for-index and an unknown source reads as an empty one.
	for len(came) < len(kept)-1 {
		came = append(came, Source{})
	}
	came = append(came, from)

	if drop := crowded(kept); drop >= 0 {
		kept = append(kept[:drop:drop], kept[drop+1:]...)
		came = append(came[:drop:drop], came[drop+1:]...)
	}

	prints, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	where, err := json.Marshal(came)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO people (name, voiceprints, sources) VALUES (?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET voiceprints = excluded.voiceprints,
		                                 sources = excluded.sources`,
		name, string(prints), string(where))
	return err
}

// Paint sets a person's colour, or clears it back to the derived one.
//
// Derived from the name means Olena is the same colour in every meeting without
// anybody choosing; this is for when the owner already pictures somebody in a
// particular colour and the derivation disagrees.
func (d *DB) PaintPerson(name, colour string) error {
	_, err := d.sql.Exec(`UPDATE people SET colour = ? WHERE name = ?`,
		strings.TrimSpace(colour), name)
	return err
}

// Sample is the moment a voiceprint was taken from, filled in with the meeting
// title and the longest thing that person said in it — which is the stretch
// worth listening to when checking whether the app has the right voice.
func (d *DB) Sample(from Source) (Source, bool) {
	if from.Recording == 0 {
		return from, false
	}
	err := d.sql.QueryRow(`
		SELECT r.title, r.audio, t.start, t.finish
		FROM turns t JOIN recordings r ON r.id = t.recording
		WHERE t.recording = ? AND t.speaker = ?
		ORDER BY (t.finish - t.start) DESC LIMIT 1`,
		from.Recording, from.Speaker).
		Scan(&from.Title, &from.Audio, &from.Start, &from.Finish)
	if err != nil {
		// The speaker was renamed since, so the label no longer matches a turn.
		// The meeting is still worth naming even without a moment inside it.
		if e := d.sql.QueryRow(`SELECT title, audio FROM recordings WHERE id = ?`,
			from.Recording).Scan(&from.Title, &from.Audio); e != nil {
			return from, false
		}
	}
	return from, true
}

// SaveVoices keeps this recording's voiceprints, one per speaker label as the
// transcript shows it. They are what makes naming a speaker afterwards teach
// the app a voice rather than only relabel four hundred rows.
// An empty map is written, not skipped. These are this recording's voices as
// of now, and a transcription that found none has to say so: transcribing again
// renumbers the clusters, so prints left over from the previous run are keyed to
// labels that belong to different people. Naming a speaker then enrols the
// wrong voice, and every later meeting inherits the mistake.
func (d *DB) SaveVoices(recording int64, prints map[string][]float32) error {
	if prints == nil {
		prints = map[string][]float32{}
	}
	encoded, err := json.Marshal(prints)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`UPDATE recordings SET voices = ? WHERE id = ?`, string(encoded), recording)
	return err
}

// VoiceIn is the voiceprint of one speaker in one recording, or nil when that
// speaker said too little to have one.
func (d *DB) VoiceIn(recording int64, speaker string) []float32 {
	var raw sql.NullString
	if err := d.sql.QueryRow(`SELECT voices FROM recordings WHERE id = ?`, recording).Scan(&raw); err != nil {
		return nil
	}
	var prints map[string][]float32
	if json.Unmarshal([]byte(raw.String), &prints) != nil {
		return nil
	}
	return prints[speaker]
}

// Forget removes a person entirely, which is the only way to undo a name that
// was given to the wrong voice.
func (d *DB) Forget(name string) error {
	_, err := d.sql.Exec(`DELETE FROM people WHERE name = ?`, name)
	return err
}

// crowded names the voiceprint that teaches the least — the sample nearest
// another sample, since a near-duplicate adds nothing that its twin did not
// already say. Ties drop the older of the pair, so a fresh sample displaces the
// stale twin and somebody who changed headset is learnt again instead of being
// remembered as they sounded on day one.
//
// It returns an index rather than a filtered slice because each print now has a
// source beside it, and the two arrays have to be cut in the same place.
// Returns -1 when there is still room.
func crowded(samples [][]float32) int {
	if len(samples) <= Keep {
		return -1
	}
	worst, twin := -2.0, 0
	for i := range samples {
		for j := range samples {
			if i == j {
				continue
			}
			if c := Cosine(samples[i], samples[j]); c > worst {
				worst, twin = c, min(i, j)
			}
		}
	}
	return twin
}

// Recognise pairs the voiceprints of one recording with the people already
// enrolled: best match first, and one label per person.
//
// Greedy rather than optimal. The alternative is the Hungarian algorithm for a
// problem that is almost always three voices against five names, where the two
// agree, and where being wrong is one click to correct.
func Recognise(prints map[string][]float32, people []Person) map[string]string {
	type pair struct {
		label string
		who   int
		score float64
	}
	var pairs []pair
	for label, print := range prints {
		for i, person := range people {
			best := -1.0
			for _, sample := range person.Voiceprints {
				if c := Cosine(print, sample); c > best {
					best = c
				}
			}
			if best >= Match {
				pairs = append(pairs, pair{label, i, best})
			}
		}
	}
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].score > pairs[b].score })

	names, takenLabel, takenPerson := map[string]string{}, map[string]bool{}, map[int]bool{}
	for _, p := range pairs {
		if takenLabel[p.label] || takenPerson[p.who] {
			continue
		}
		takenLabel[p.label], takenPerson[p.who] = true, true
		names[p.label] = people[p.who].Name
	}
	return names
}

// Cosine is the similarity of two voiceprints, in -1..1. Two recordings of the
// same person sit around 0.7; two different people sit near 0.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return -1
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return -1
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// RawVoices is the stored voiceprints of a recording, as written. Only the
// probe in this package's cmd uses it.
func (d *DB) RawVoices(recording int64) string {
	var raw sql.NullString
	_ = d.sql.QueryRow(`SELECT voices FROM recordings WHERE id = ?`, recording).Scan(&raw)
	return raw.String
}

// Trace fills in where existing voiceprints came from.
//
// Sources were added after people had already been enrolled, so the samples
// already saved are bare vectors. They are not lost, though: the identical
// vector is still sitting in some recording's `voices`, which is where it was
// copied from. Matching them back is exact — the same float32s, not a
// similarity — so a sample either finds its meeting or honestly has none.
//
// Runs once and then does nothing, because after it every source is filled.
func (d *DB) Trace() error {
	people, err := d.People()
	if err != nil {
		return err
	}
	missing := false
	for _, p := range people {
		for i := range p.Voiceprints {
			if i >= len(p.Sources) || p.Sources[i].Recording == 0 {
				missing = true
			}
		}
	}
	if !missing {
		return nil
	}

	rows, err := d.sql.Query(`SELECT id, voices FROM recordings WHERE voices IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type heard struct {
		recording int64
		speaker   string
		print     []float32
	}
	var all []heard
	for rows.Next() {
		var id int64
		var raw sql.NullString
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		var prints map[string][]float32
		if json.Unmarshal([]byte(raw.String), &prints) != nil {
			continue
		}
		for speaker, print := range prints {
			all = append(all, heard{id, speaker, print})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range people {
		found := make([]Source, len(p.Voiceprints))
		copy(found, p.Sources)
		for i, print := range p.Voiceprints {
			if found[i].Recording != 0 {
				continue
			}
			for _, h := range all {
				if same(print, h.print) {
					found[i] = Source{Recording: h.recording, Speaker: h.speaker}
					break
				}
			}
		}
		encoded, err := json.Marshal(found)
		if err != nil {
			continue
		}
		if _, err := d.sql.Exec(`UPDATE people SET sources = ? WHERE id = ?`,
			string(encoded), p.ID); err != nil {
			return err
		}
	}
	return nil
}

// same is exact equality, not similarity: these are copies of one another or
// they are unrelated.
func same(a, b []float32) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Appearances is the projects one person has been heard in, most meetings
// first. Unfiled meetings are counted under the zero group, so somebody who
// only ever appears outside a project still says so.
func (d *DB) Appearances(name string) ([]Group, error) {
	rows, err := d.sql.Query(`
		SELECT COALESCE(g.id, 0), COALESCE(g.name, ''), COALESCE(g.colour, ''),
		       COUNT(DISTINCT r.id)
		FROM turns t
		JOIN recordings r ON r.id = t.recording AND r.deleted IS NULL
		LEFT JOIN groups g ON g.id = r.folder
		WHERE t.speaker = ?
		GROUP BY g.id ORDER BY COUNT(DISTINCT r.id) DESC, g.name`, name)
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

// Same rejoins the labels of one recording that belong to one enrolled person.
// This is what lets the clusterer split too eagerly: splitting is recoverable
// here, merging two people never is.
//
// Labels matching nobody are left alone — a stranger stays their own speaker.
//
// Resembling the same person is necessary but not sufficient. At Match alone,
// several genuinely different people clear the bar for whoever they resemble
// most, and a six-voice meeting came back as two with the owner folded in. So
// the clusters must also sound like each other.
func Same(prints map[string][]float32, people []Person) map[string]string {
	best := map[string]struct {
		who   string
		score float64
	}{}
	for label, print := range prints {
		for _, person := range people {
			for _, sample := range person.Voiceprints {
				c := Cosine(print, sample)
				if c >= Match && c > best[label].score {
					best[label] = struct {
						who   string
						score float64
					}{person.Name, c}
				}
			}
		}
	}
	// One label per person keeps the name: the rest are told to become it.
	keeper := map[string]string{}
	for label, m := range best {
		if m.who == "" {
			continue
		}
		if held, taken := keeper[m.who]; !taken || best[held].score < m.score {
			keeper[m.who] = label
		}
	}
	same := map[string]string{}
	for label, m := range best {
		to := keeper[m.who]
		if m.who == "" || to == label {
			continue
		}
		// Logged either way. When a meeting comes back with the wrong number of
		// people this is the line that says whether the clusterer or this is to
		// blame, and it is nearly impossible to work out afterwards without it.
		alike := Cosine(prints[label], prints[to])
		slog.Info("two clusters resemble one person",
			"label", label, "into", to, "who", m.who,
			"alike", alike, "joined", alike >= Rejoin)
		if alike >= Rejoin {
			same[label] = to
		}
	}
	return same
}
