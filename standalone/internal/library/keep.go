package library

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/store"
)

// Sweep deletes audio the settings say has stopped being interesting.
//
// Transcripts are kept for ever — they are text, and text is free. Audio is
// not: an hour of stereo at 16 kHz is 230 MB, and a folder that grows for ever
// is a promise the app cannot keep on somebody's laptop.
//
// The recording row stays, with its transcript, its summary and its analytics.
// Only the file goes, and the row says so.
func Sweep(db *store.DB, dir string, days int) (int, int64, error) {
	if days <= 0 {
		return 0, 0, nil // 0 means keep everything, which is a real answer
	}
	recent, err := db.Recent(1000)
	if err != nil {
		return 0, 0, err
	}
	cutoff := time.Now().AddDate(0, 0, -days)

	gone, freed := 0, int64(0)
	for _, r := range recent {
		// Nothing is deleted before it has been read: a recording that failed
		// or is still queued is the one case where the audio is all there is.
		if r.Audio == "" || r.Status != store.Done || r.Started.After(cutoff) {
			continue
		}
		path := filepath.Join(dir, r.Audio)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if err := os.Remove(path); err != nil {
			slog.Warn("could not delete old audio", "file", r.Audio, "err", err)
			continue
		}
		// The folded copy the player was served is derived from it and has no
		// reason to outlive it.
		_ = os.Remove(filepath.Join(filepath.Dir(dir), "cache", r.Audio))
		if err := db.Dropped(r.ID); err != nil {
			slog.Warn("deleted the audio but could not record it", "id", r.ID, "err", err)
		}
		gone++
		freed += info.Size()
	}
	if gone > 0 {
		slog.Info("old audio deleted", "files", gone, "mb", freed/(1<<20), "older_than_days", days)
	}
	return gone, freed, nil
}

// Tidy runs Sweep now and once a day after that. Daily rather than hourly
// because the setting is measured in days: checking more often can only find
// the same nothing.
func (l *Library) Tidy(stop <-chan struct{}, days func() int) {
	for {
		if _, _, err := Sweep(l.db, l.dir, days()); err != nil {
			slog.Warn("could not tidy the recordings folder", "err", err)
		}
		select {
		case <-stop:
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
