// Package upload moves finished recordings from the spool directory to the
// server, and is the only part of the daemon that knows the server exists.
//
// The spool is the daemon's entire persistent state. There is no database and
// no queue: a file on disk is a recording waiting to be sent, and it is deleted
// once the server has accepted it. That is what lets the daemon be killed at
// any moment, and what lets it keep working while the server is down.
package upload

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// stamp is both the sort order and the metadata: sorting the spool by name
	// sends the oldest recording first, and the kind is in the name, so no
	// sidecar file has to be kept in step with the audio.
	stamp     = "2006-01-02T15-04-05"
	display   = "2006-01-02 15:04"
	retryFrom = 5 * time.Second
	retryTo   = 5 * time.Minute
)

// Uploader watches a spool directory.
type Uploader struct {
	dir    string
	server string
	client *http.Client
	wake   chan struct{}

	mu      sync.Mutex
	failing string // why the last attempt failed, for the tray
}

func New(dir, server string) (*Uploader, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("spool: %w", err)
	}
	return &Uploader{
		dir:    dir,
		server: strings.TrimRight(server, "/"),
		// Long, because the server holds the request open while it writes the
		// upload to disk, and an hour of audio is not a small file.
		client: &http.Client{Timeout: 10 * time.Minute},
		wake:   make(chan struct{}, 1),
	}, nil
}

// Path is where a recording of this kind, started at this time, should be
// written. The caller writes the audio; the uploader takes it from there.
func (u *Uploader) Path(kind string, at time.Time) string {
	return filepath.Join(u.dir, fmt.Sprintf("%s_%s.wav", kind, at.Format(stamp)))
}

// Wake asks for an immediate sweep, which is what a finished recording does
// rather than waiting out the poll interval.
func (u *Uploader) Wake() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Pending counts recordings still waiting to be sent, for the tray.
func (u *Uploader) Pending() int {
	found, _ := u.spooled()
	return len(found)
}

// Trouble is what is wrong with the app, in words a person can act on, or empty
// when nothing is. Silently spooling for ever is the failure a background
// program is most likely to hide.
func (u *Uploader) Trouble() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.failing
}

// Server is where recordings go, so the tray can offer to open it.
func (u *Uploader) Server() string { return u.server }

func (u *Uploader) note(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case err == nil:
		u.failing = ""
	case strings.Contains(err.Error(), "connection refused"),
		strings.Contains(err.Error(), "no such host"),
		strings.Contains(err.Error(), "timeout"):
		u.failing = "cannot reach the app"
	default:
		u.failing = "the app refused a recording"
	}
}

// Run sends recordings until the context is cancelled.
//
// busy is asked before every sweep and, when it says yes, the sweep is skipped:
// the server starts Whisper the moment it accepts a file, and making the machine
// transcribe the first hour of a meeting while the second hour is still being
// recorded is how a laptop turns into a heater during a call.
func (u *Uploader) Run(ctx context.Context, busy func() bool) {
	if u.server == "" {
		slog.Warn("no server configured; recordings will collect in the spool", "dir", u.dir)
		return
	}
	backoff := retryFrom
	for {
		if !busy() {
			sent, err := u.sweep(ctx)
			u.note(err)
			switch {
			case err != nil:
				slog.Warn("upload failed; will retry", "in", backoff, "err", err)
				backoff = min(backoff*2, retryTo)
			default:
				backoff = retryFrom
				if sent > 0 {
					slog.Info("uploaded", "recordings", sent, "pending", u.Pending())
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-u.wake:
		case <-time.After(backoff):
		}
	}
}

// sweep sends every spooled recording, oldest first, stopping at the first
// failure so that order is preserved and a dead server is not hammered.
func (u *Uploader) sweep(ctx context.Context) (int, error) {
	found, err := u.spooled()
	if err != nil {
		return 0, err
	}
	for i, path := range found {
		if err := ctx.Err(); err != nil {
			return i, nil
		}
		if err := u.send(ctx, path); err != nil {
			return i, err
		}
		if err := os.Remove(path); err != nil {
			// Leaving it would send the same meeting again on the next sweep.
			return i, fmt.Errorf("accepted but not removed: %w", err)
		}
	}
	return len(found), nil
}

func (u *Uploader) spooled() ([]string, error) {
	found, err := filepath.Glob(filepath.Join(u.dir, "*.wav"))
	sort.Strings(found) // the timestamp in the name is the order
	return found, err
}

// send posts one recording to the endpoint its kind calls for.
func (u *Uploader) send(ctx context.Context, path string) error {
	kind, at, err := parse(filepath.Base(path))
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// The name here becomes the title in the Library, so it is written the way
	// a person reads it rather than the way the spool sorts it.
	name := fmt.Sprintf("%s %s.wav", kind, at.Format(display))
	body, contentType, err := multipartBody(name, file)
	if err != nil {
		return err
	}

	endpoint := "/v1/transcribe"
	if kind == "meeting" {
		endpoint = "/v1/transcribe-diarize"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.server+endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)

	started := time.Now()
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s returned %s: %s", endpoint, resp.Status, bytes.TrimSpace(detail))
	}
	slog.Info("sent", "recording", name, "to", endpoint, "took", time.Since(started).Round(time.Millisecond))
	return nil
}

// multipartBody buffers the request. Streaming would be tidier, but the server
// answers 413 and 415 before reading everything, and a streamed body turns those
// clean refusals into broken pipes.
func multipartBody(name string, file io.Reader) (io.Reader, string, error) {
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		return nil, "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, "", err
	}
	if err := form.Close(); err != nil {
		return nil, "", err
	}
	return &buf, form.FormDataContentType(), nil
}

// parse pulls the kind and the start time back out of a spool file name.
func parse(base string) (kind string, at time.Time, err error) {
	name := strings.TrimSuffix(base, ".wav")
	k, ts, found := strings.Cut(name, "_")
	if !found {
		return "", time.Time{}, fmt.Errorf("unrecognised spool file %q", base)
	}
	if k != "meeting" && k != "note" {
		return "", time.Time{}, fmt.Errorf("unknown kind %q in %q", k, base)
	}
	if at, err = time.ParseInLocation(stamp, ts, time.Local); err != nil {
		return "", time.Time{}, fmt.Errorf("unrecognised timestamp in %q: %w", base, err)
	}
	return k, at, nil
}
