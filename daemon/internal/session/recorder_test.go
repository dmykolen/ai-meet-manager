package session

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/source"
	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/vad"
)

// The VAD in these tests is the real Silero, not a stub. A stub that answered
// "speech" on demand would test the plumbing and hide the thing most likely to
// be wrong, which is whether real audio is heard as speech at all.
func TestMain(m *testing.M) {
	root, _ := filepath.Abs("../../runtime")
	os.Setenv("MTD_SILERO_VAD", filepath.Join(root, "silero_vad.onnx"))
	if _, err := os.Stat(filepath.Join(root, "onnxruntime.dylib")); err == nil {
		os.Setenv("MTD_ONNXRUNTIME", filepath.Join(root, "onnxruntime.dylib"))
	}
	os.Exit(m.Run())
}

// spoolDir is the uploader's side of the contract, and nothing more of it.
type spoolDir struct {
	dir   string
	mu    sync.Mutex
	woken int
}

func (s *spoolDir) Path(kind string, at time.Time) string {
	return filepath.Join(s.dir, kind+"_"+at.Format("2006-01-02T15-04-05.000000000")+".wav")
}

func (s *spoolDir) Wake() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.woken++
}

func (s *spoolDir) files(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(s.dir, "*.wav"))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// speech reads the recording the daemon made of itself, looped to length.
func speech(t *testing.T, d time.Duration) []int16 {
	t.Helper()
	raw, err := os.ReadFile("../vad/testdata/speech.wav")
	if err != nil {
		t.Skipf("no speech fixture: %v", err)
	}
	body := raw[44:]
	clip := make([]int16, len(body)/2)
	for i := range clip {
		clip[i] = int16(binary.LittleEndian.Uint16(body[2*i:]))
	}
	// Loop against a fixed target, never against cap(out): append grows the
	// capacity, so `len(out) < cap(out)` would keep being true and the slice
	// would grow until the machine gave up.
	want := int(d.Seconds() * float64(source.SampleRate))
	out := make([]int16, 0, want)
	for len(out) < want {
		out = append(out, clip...)
	}
	return out[:want]
}

// play feeds frames built from the given mono channels. A nil channel is
// silence, which is how "nobody else is talking" is expressed.
func play(frames chan<- []int16, mic, sys []int16, d time.Duration) {
	total := int(d.Seconds() * float64(source.SampleRate))
	for off := 0; off+source.FrameSize <= total; off += source.FrameSize {
		frame := make([]int16, 2*source.FrameSize)
		for i := range source.FrameSize {
			if mic != nil {
				frame[2*i] = mic[(off+i)%len(mic)]
			}
			if sys != nil {
				frame[2*i+1] = sys[(off+i)%len(sys)]
			}
		}
		frames <- frame
	}
}

func recorder(t *testing.T) (*Recorder, *spoolDir) {
	t.Helper()
	spool := &spoolDir{dir: t.TempDir()}
	// A short ring, because the tests care that the preroll happens, not that
	// it is ten minutes long.
	r, err := NewRecorder(spool, 30*time.Second, 5*time.Second, Tuning{})
	if err != nil {
		t.Skipf("Silero unavailable: %v", err)
	}
	return r, spool
}

// run drives the recorder over a script of segments and returns once they are
// all consumed.
func run(t *testing.T, r *Recorder, script func(chan<- []int16)) {
	t.Helper()
	frames := make(chan []int16, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, frames) }()

	script(frames)
	close(frames)
	if err := <-done; err != nil {
		t.Fatalf("recorder: %v", err)
	}
}

func TestARealConversationIsRecordedAsAMeeting(t *testing.T) {
	r, spool := recorder(t)
	talk := speech(t, 4*time.Second)

	run(t, r, func(frames chan<- []int16) {
		play(frames, talk, talk, StartSpeech+5*time.Second) // both sides talking
		play(frames, nil, nil, QuietMeeting+2*time.Second)  // then everyone stops
	})

	files := spool.files(t)
	if len(files) != 1 {
		t.Fatalf("spooled %v, want one recording", files)
	}
	if base := filepath.Base(files[0]); base[:7] != "meeting" {
		t.Fatalf("spooled %q; speech on the system channel is a meeting", base)
	}
	if spool.woken == 0 {
		t.Fatal("the uploader was never told there was something to send")
	}
}

func TestTalkingAloneIsRecordedAsANote(t *testing.T) {
	r, spool := recorder(t)
	talk := speech(t, 4*time.Second)

	run(t, r, func(frames chan<- []int16) {
		play(frames, talk, nil, StartSpeech+5*time.Second) // nobody on the other end
		play(frames, nil, nil, QuietNote+2*time.Second)
	})

	files := spool.files(t)
	if len(files) != 1 {
		t.Fatalf("spooled %v, want one recording", files)
	}
	if base := filepath.Base(files[0]); base[:4] != "note" {
		t.Fatalf("spooled %q; a monologue is a note", base)
	}
}

func TestSilenceIsNeverRecorded(t *testing.T) {
	r, spool := recorder(t)
	run(t, r, func(frames chan<- []int16) {
		play(frames, nil, nil, 2*time.Minute)
	})
	if files := spool.files(t); len(files) != 0 {
		t.Fatalf("spooled %v from an empty room", files)
	}
}

func TestTheRecordingReachesBackBeforeItStarted(t *testing.T) {
	// The reason the daemon exists: by the time anything can tell a meeting is
	// happening, it has been happening for a while.
	r, spool := recorder(t)
	talk := speech(t, 4*time.Second)

	run(t, r, func(frames chan<- []int16) {
		// Silero calls about 88% of frames in real speech "speech", so twenty
		// seconds of talking is not twenty seconds of detections. The margin is
		// deliberate: this test is about the preroll, not about the threshold.
		play(frames, talk, talk, StartSpeech+10*time.Second)
		play(frames, nil, nil, QuietMeeting+2*time.Second)
	})

	files := spool.files(t)
	if len(files) != 1 {
		t.Fatalf("spooled %v", files)
	}
	held := wavSeconds(t, files[0])
	// Detection eats the first ~23s of talking before it fires (Silero calls
	// about 88% of speech frames speech, so StartSpeech needs more than
	// StartSpeech of audio). Of the 30s played, roughly 7s arrive after the
	// trigger — so anything much past that came out of the ring.
	const afterTheTrigger = 7.0
	if held < afterTheTrigger+3 {
		t.Fatalf("the recording is %.1fs, barely more than the %.0fs that followed the "+
			"trigger; the preroll did not make it in", held, afterTheTrigger)
	}
	t.Logf("recording is %.1fs, of which about %.1fs was replayed from the ring",
		held, held-afterTheTrigger)
}

func TestPausingStopsRecordingAndClosesTheFile(t *testing.T) {
	r, spool := recorder(t)
	talk := speech(t, 4*time.Second)

	run(t, r, func(frames chan<- []int16) {
		play(frames, talk, talk, StartSpeech+10*time.Second)
		r.Pause(true)
		play(frames, talk, talk, 10*time.Second) // ignored while paused
	})

	if got := r.Status().Phase; got != Paused {
		t.Fatalf("phase is %q after pausing", got)
	}
	if files := spool.files(t); len(files) != 1 {
		t.Fatalf("spooled %v; pausing must close what was open", files)
	}
}

func TestTheTrayIsToldWhatIsHappening(t *testing.T) {
	r, _ := recorder(t)
	// Opening, not Listening: the devices are not up yet, and the menu bar
	// appears before they are. Claiming to listen first would be a lie, and the
	// lie people notice is the one told while a permission dialog is waiting.
	if got := r.Status().Phase; got != Opening {
		t.Fatalf("a fresh recorder reports %q, want %q", got, Opening)
	}
	r.Report(Listening)
	if got := r.Status().Phase; got != Listening {
		t.Fatalf("after Report the recorder says %q", got)
	}
	talk := speech(t, 4*time.Second)
	run(t, r, func(frames chan<- []int16) {
		play(frames, talk, talk, StartSpeech+10*time.Second)
		if got := r.Status().Phase; got != Recording {
			t.Errorf("phase is %q while people are talking", got)
		}
		if !r.Busy() {
			t.Error("Busy() is false during a recording; uploads would start mid-meeting")
		}
	})
}

func TestAFrameIsNotWrittenWhileNothingIsRecording(t *testing.T) {
	r, spool := recorder(t)
	// Below the threshold: loud, but not for long enough.
	run(t, r, func(frames chan<- []int16) {
		play(frames, speech(t, 4*time.Second), nil, StartSpeech-5*time.Second)
	})
	if files := spool.files(t); len(files) != 0 {
		t.Fatalf("spooled %v from %v of speech", files, StartSpeech-5*time.Second)
	}
}

func wavSeconds(t *testing.T, path string) float64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	const header, bytesPerFrame = 44, 4 // stereo int16
	return float64(info.Size()-header) / bytesPerFrame / float64(source.SampleRate)
}

func TestTheFrameSizeIsSileroWindow(t *testing.T) {
	// The recorder hands whole frames straight to the VAD. If these ever drift
	// apart, every frame is refused and the daemon records nothing at all.
	if source.FrameSize != vad.Window {
		t.Fatalf("capture frame is %d samples, Silero wants %d", source.FrameSize, vad.Window)
	}
}

func TestRecordNowFromTheTrayProducesARecording(t *testing.T) {
	// The tray runs on its own goroutine and only leaves a note; this checks
	// the note is picked up by the capture loop and acted on.
	r, spool := recorder(t)

	frames := make(chan []int16, 8)
	done := make(chan error, 1)
	go func() { done <- r.Run(t.Context(), frames) }()

	play(frames, nil, nil, time.Second) // a silent room
	r.Toggle()
	play(frames, nil, nil, 3*time.Second)

	if !r.Recording() {
		t.Fatal("Record now did not start anything")
	}
	r.Toggle()
	play(frames, nil, nil, time.Second)

	close(frames)
	<-done
	if files := spool.files(t); len(files) != 1 {
		t.Fatalf("spooled %v, want the one recording that was asked for", files)
	}
}

func TestTheTrayCanBeToldThatPermissionIsMissing(t *testing.T) {
	// The state a person most needs to see, and the one that used to show as no
	// menu bar icon at all.
	r, _ := recorder(t)
	r.Report(NeedsPermission)
	if got := r.Status().Phase; got != NeedsPermission {
		t.Fatalf("phase is %q", got)
	}
	if r.Recording() {
		t.Fatal("nothing can be recording while the devices are shut")
	}
}

func TestTheQuietTailIsNotRecorded(t *testing.T) {
	// Measured on a real meeting: it ended at 40.9 minutes and the file ran to
	// 65, because the detector needs three minutes of silence to be sure. Whisper
	// filled the gap with "Продолжение следует..." fourteen times.
	r, spool := recorder(t)
	talk := speech(t, 4*time.Second)

	run(t, r, func(frames chan<- []int16) {
		play(frames, talk, talk, StartSpeech+10*time.Second)
		play(frames, nil, nil, QuietMeeting+5*time.Second)
	})

	files := spool.files(t)
	if len(files) != 1 {
		t.Fatalf("spooled %v", files)
	}
	held := wavSeconds(t, files[0])
	// The talking was about 30s plus the preroll; the three minutes of silence
	// that ended it must not be in the file.
	if held > 90 {
		t.Fatalf("the recording is %.0fs — the quiet tail was kept", held)
	}
	t.Logf("recording is %.0fs of talking, with %v of trailing silence dropped",
		held, QuietMeeting)
}

func TestAPauseInsideAMeetingIsKept(t *testing.T) {
	// Only the trailing silence goes. A pause in the middle is part of it.
	r, spool := recorder(t)
	talk := speech(t, 4*time.Second)

	run(t, r, func(frames chan<- []int16) {
		play(frames, talk, talk, StartSpeech+10*time.Second)
		play(frames, nil, nil, 30*time.Second) // a pause, not the end
		play(frames, talk, talk, 10*time.Second)
		play(frames, nil, nil, QuietMeeting+5*time.Second)
	})

	// 5s preroll + ~7s of speech after the trigger + 30s pause + 10s speech.
	// Without the pause it would be about 22s, so 45 is comfortably on the right
	// side of the question this test asks.
	held := wavSeconds(t, spool.files(t)[0])
	if held < 45 {
		t.Fatalf("the recording is only %.0fs — the pause in the middle was dropped too", held)
	}
	t.Logf("recording is %.0fs, with the 30s pause kept and the trailing silence gone", held)
}
