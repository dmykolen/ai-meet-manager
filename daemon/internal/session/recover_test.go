package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAnUnfinishedRecordingIsInvisibleToTheUploader(t *testing.T) {
	// The uploader only looks at *.wav. While a meeting is being recorded its
	// file must not match that, or a half-written hour could be sent mid-call.
	r, spool := recorder(t)
	talk := speech(t, 4*time.Second)

	frames := make(chan []int16, 8)
	done := make(chan error, 1)
	go func() { done <- r.Run(t.Context(), frames) }()

	play(frames, talk, talk, StartSpeech+10*time.Second)
	if files := spool.files(t); len(files) != 0 {
		t.Fatalf("a recording in progress is already visible as %v", files)
	}
	partials, _ := filepath.Glob(filepath.Join(spool.dir, "*"+partial))
	if len(partials) != 1 {
		t.Fatalf("found %v; the open recording should be a .part file", partials)
	}

	close(frames)
	<-done
	if files := spool.files(t); len(files) != 1 {
		t.Fatalf("after finishing, the spool holds %v", files)
	}
}

func TestAnInterruptedRecordingIsDiscardedAndReported(t *testing.T) {
	// A .part file left by a daemon that died mid-write has an unpatched header
	// and holds no readable audio. Leaving it would puzzle somebody later.
	dir := t.TempDir()
	stale := filepath.Join(dir, "meeting_2026-08-30T10-00-00.wav"+partial)
	if err := os.WriteFile(stale, make([]byte, 44+4*16000), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "meeting_2026-08-30T09-00-00.wav")
	os.WriteFile(keep, []byte("finished"), 0o644)

	Recover(dir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("the interrupted recording is still there")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("Recover deleted a finished recording")
	}
}
