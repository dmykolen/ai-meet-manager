package upload

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// server records what the daemon posted, and answers however the test wants.
type server struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	paths  []string
	names  []string
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{status: http.StatusAccepted}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, header, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		file.Close()
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.names = append(s.names, header.Filename)
		status := s.status
		s.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) refuse(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *server) seen() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...), append([]string(nil), s.names...)
}

func spool(t *testing.T, u *Uploader, kind string, at time.Time) string {
	t.Helper()
	path := u.Path(kind, at)
	if err := os.WriteFile(path, []byte("RIFF....WAVEfmt "), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func uploader(t *testing.T, addr string) *Uploader {
	t.Helper()
	u, err := New(t.TempDir(), addr)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func idle() bool { return false }

func TestAMeetingIsDiarizedAndANoteIsNot(t *testing.T) {
	srv := newServer(t)
	u := uploader(t, srv.URL)
	spool(t, u, "meeting", time.Date(2026, 8, 30, 15, 32, 11, 0, time.Local))
	spool(t, u, "note", time.Date(2026, 8, 30, 16, 0, 0, 0, time.Local))

	if _, err := u.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	paths, names := srv.seen()
	if len(paths) != 2 || paths[0] != "/v1/transcribe-diarize" || paths[1] != "/v1/transcribe" {
		t.Fatalf("posted to %v; a meeting gets speakers and a note does not", paths)
	}
	if names[0] != "meeting 2026-08-30 15:32.wav" {
		t.Fatalf("Library would show %q", names[0])
	}
}

func TestAnAcceptedRecordingIsRemovedFromTheSpool(t *testing.T) {
	srv := newServer(t)
	u := uploader(t, srv.URL)
	path := spool(t, u, "note", time.Now())

	if _, err := u.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the recording is still in the spool after the server accepted it")
	}
	if u.Pending() != 0 {
		t.Fatalf("%d still pending", u.Pending())
	}
}

func TestARefusedRecordingIsKept(t *testing.T) {
	// The whole point of the spool: a server that is down or unhappy must not
	// cost anybody a meeting.
	srv := newServer(t)
	srv.refuse(http.StatusInternalServerError)
	u := uploader(t, srv.URL)
	path := spool(t, u, "meeting", time.Now())

	if _, err := u.sweep(context.Background()); err == nil {
		t.Fatal("a 500 was reported as success")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the recording was lost: %v", err)
	}
}

func TestNothingIsSentWhileTheServerIsUnreachable(t *testing.T) {
	u := uploader(t, "http://127.0.0.1:1") // nothing listens there
	path := spool(t, u, "meeting", time.Now())

	if _, err := u.sweep(context.Background()); err == nil {
		t.Fatal("an unreachable server was reported as success")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the recording was lost: %v", err)
	}
}

func TestTheOldestRecordingGoesFirst(t *testing.T) {
	srv := newServer(t)
	u := uploader(t, srv.URL)
	base := time.Date(2026, 8, 30, 9, 0, 0, 0, time.Local)
	spool(t, u, "note", base.Add(2*time.Hour))
	spool(t, u, "note", base)
	spool(t, u, "note", base.Add(time.Hour))

	if _, err := u.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, names := srv.seen()
	want := []string{"note 2026-08-30 09:00.wav", "note 2026-08-30 10:00.wav", "note 2026-08-30 11:00.wav"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("sent %v, want %v", names, want)
		}
	}
}

func TestOneFailureStopsTheSweepSoOrderIsKept(t *testing.T) {
	srv := newServer(t)
	srv.refuse(http.StatusInternalServerError)
	u := uploader(t, srv.URL)
	base := time.Date(2026, 8, 30, 9, 0, 0, 0, time.Local)
	spool(t, u, "note", base)
	spool(t, u, "note", base.Add(time.Hour))

	u.sweep(context.Background())
	if paths, _ := srv.seen(); len(paths) != 1 {
		t.Fatalf("%d recordings attempted; the sweep must stop at the first failure", len(paths))
	}
	if u.Pending() != 2 {
		t.Fatalf("%d pending, want both kept", u.Pending())
	}
}

func TestNothingIsUploadedWhileARecordingIsRunning(t *testing.T) {
	// Uploading mid-meeting makes the server start Whisper while the meeting is
	// still going, which is exactly when the machine is least able to afford it.
	srv := newServer(t)
	u := uploader(t, srv.URL)
	spool(t, u, "meeting", time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); u.Run(ctx, func() bool { return true }) }()

	u.Wake()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	if paths, _ := srv.seen(); len(paths) != 0 {
		t.Fatalf("uploaded %v during a recording", paths)
	}
	if u.Pending() != 1 {
		t.Fatal("the recording should still be waiting")
	}
}

func TestRunSendsOnceTheRecordingStops(t *testing.T) {
	srv := newServer(t)
	u := uploader(t, srv.URL)
	spool(t, u, "meeting", time.Now())

	var recording sync.Mutex
	recording.Lock()
	busy := func() bool { return !recording.TryLock() }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx, busy)

	recording.Unlock()
	u.Wake()
	for range 40 {
		if u.Pending() == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the recording was never sent after the meeting ended")
}

func TestAnUnrecognisedSpoolFileIsReportedNotSilentlyDropped(t *testing.T) {
	srv := newServer(t)
	u := uploader(t, srv.URL)
	stray := filepath.Join(t.TempDir(), "stray.wav")
	os.WriteFile(stray, []byte("x"), 0o644)
	os.Rename(stray, filepath.Join(u.dir, "whatever.wav"))

	if _, err := u.sweep(context.Background()); err == nil {
		t.Fatal("a file the daemon cannot interpret was passed over in silence")
	}
}
