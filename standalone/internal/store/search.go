package store

import (
	"fmt"
	"strings"
)

// Hit is one passage that matched a search.
type Hit struct {
	Recording int64   `json:"recording"`
	Title     string  `json:"title"`
	Start     float64 `json:"start"`
	Speaker   string  `json:"speaker,omitempty"`
	Text      string  `json:"text"`
}

// Search finds passages across every transcript.
//
// SQLite's own full-text index rather than embeddings: it is already in the file,
// it needs no key and no network, and for "find where somebody said Vodafone" it
// is the right tool. Meaning-based search is what Ask is for, and Ask uses these
// hits as its passages.
func (d *DB) Search(query string, limit int) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 30
	}
	rows, err := d.sql.Query(`
		SELECT t.recording, r.title, t.start, t.speaker, t.text
		FROM transcript t JOIN recordings r ON r.id = t.recording
		WHERE transcript MATCH ?
		ORDER BY rank
		LIMIT ?`, fts(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Recording, &h.Title, &h.Start, &h.Speaker, &h.Text); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// fts turns what somebody typed into something FTS5 will accept.
//
// Every word is quoted and the last gets a prefix star, so "vodafone доку"
// matches "документи" while an apostrophe or a stray quote cannot end the query
// and start a new one.
func fts(query string) string {
	words := strings.Fields(query)
	quoted := make([]string, 0, len(words))
	for i, w := range words {
		w = strings.ReplaceAll(w, `"`, `""`)
		if i == len(words)-1 {
			quoted = append(quoted, fmt.Sprintf(`"%s"*`, w))
			continue
		}
		quoted = append(quoted, fmt.Sprintf(`"%s"`, w))
	}
	return strings.Join(quoted, " ")
}

// Passages renders hits the way the model reads them: labelled with where they
// came from, so an answer can cite the meeting and the minute.
func Passages(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		who := h.Speaker
		if who == "" {
			who = "Unknown"
		}
		out = append(out, fmt.Sprintf("%s [%s] %s: %s", h.Title, clock(h.Start), who, h.Text))
	}
	return out
}

func clock(seconds float64) string {
	s := int(seconds)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}
