package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
)

// Keep is how many voiceprints one person is allowed to accumulate.
//
// Not one: a voice through a headset and the same voice through a laptop
// microphone are measurably different, and a single sample makes the app good
// at recognising somebody exactly once. More samples means more of the ways a
// person can sound — a cold, a different room, a different headset — so ten
// rather than eight.
//
// Not unbounded, and the reason is not the arithmetic: matching is a nearest
// neighbour over every sample of every person, which at ten samples each and
// fifty people is five hundred dot products of 512 floats — under a
// millisecond. The reason is that the match takes the *best* sample, so every
// extra one can only make a false positive likelier. Somebody who is nearly a
// stranger needs to be near only one of ten, and ten is where the cost of that
// starts to show.
const Keep = 10

// Match is how close two voiceprints have to be to be called the same person.
//
// Higher than the 0.9 the diarizer clusters at, because the cost of being wrong
// is different: mislabelling two speakers inside one meeting is visible and
// correctable, but putting a colleague's name on a stranger's words in the
// Library is worse than leaving them as SPEAKER_01.
const Match = 0.55

// A Person is somebody the app has been told the name of, and the voiceprints
// it has collected for them since.
type Person struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	Voiceprints [][]float32 `json:"-"`
	Samples     int         `json:"samples"`
	Meetings    int         `json:"meetings"`
}

// People is everybody enrolled, most heard first.
func (d *DB) People() ([]Person, error) {
	rows, err := d.sql.Query(`
		SELECT p.id, p.name, p.voiceprints,
		       (SELECT COUNT(DISTINCT recording) FROM turns WHERE speaker = p.name)
		FROM people p ORDER BY p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var people []Person
	for rows.Next() {
		var p Person
		var raw string
		if err := rows.Scan(&p.ID, &p.Name, &raw, &p.Meetings); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &p.Voiceprints); err != nil {
			// A corrupt row should cost one person's recognition, not the list.
			p.Voiceprints = nil
		}
		p.Samples = len(p.Voiceprints)
		people = append(people, p)
	}
	return people, rows.Err()
}

// Remember files another sample of a voice under a name, so that recognition
// improves every time somebody is named rather than staying as good as the
// first thirty seconds it ever heard.
func (d *DB) Remember(name string, print []float32) error {
	if name == "" || len(print) == 0 {
		return errors.New("nothing to remember")
	}
	var id int64
	var raw string
	err := d.sql.QueryRow(`SELECT id, voiceprints FROM people WHERE name = ?`, name).Scan(&id, &raw)

	var kept [][]float32
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		_ = json.Unmarshal([]byte(raw), &kept)
	}

	encoded, err := json.Marshal(thin(append(kept, print)))
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO people (name, voiceprints) VALUES (?, ?)
		 ON CONFLICT(name) DO UPDATE SET voiceprints = excluded.voiceprints`,
		name, string(encoded))
	return err
}

// SaveVoices keeps this recording's voiceprints, one per speaker label as the
// transcript shows it. They are what makes naming a speaker afterwards teach
// the app a voice rather than only relabel four hundred rows.
func (d *DB) SaveVoices(recording int64, prints map[string][]float32) error {
	if len(prints) == 0 {
		return nil
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

// thin makes room for one more voiceprint by dropping the one that teaches the
// least — the sample nearest another sample, since a near-duplicate adds
// nothing that its twin did not already say. Ties drop the older of the pair,
// so a fresh sample displaces the stale twin and somebody who changed headset
// is learnt again instead of being remembered as they sounded on day one.
//
// This is what makes a larger Keep safe. Without it, ten samples would be ten
// recordings of the same meeting; with it, they are ten ways the person has
// been heard to sound.
func thin(samples [][]float32) [][]float32 {
	if len(samples) <= Keep {
		return samples
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
	return append(samples[:twin:twin], samples[twin+1:]...)
}

// Recognise pairs the voiceprints of one recording with the people already
// enrolled: best match first, and nobody used twice.
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
