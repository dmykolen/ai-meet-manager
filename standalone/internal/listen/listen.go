// Package listen is the always-on part: it hears a meeting start, records it
// from before it noticed, and hands the file to the library when it ends.
//
// The file is a stereo WAV — left is the microphone, right is whatever the
// machine is playing. Keeping the two apart is what removes the need for echo
// cancellation, and it is also the entire meeting detector: speech on the right
// channel means somebody is talking to you, and that is the difference between
// a meeting and thinking aloud.
package listen

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/audio"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/home"
)

// Phase is what the app is doing, for the window to show. Every wait is a
// visible state; a pause with no name is a bug.
type Phase string

const (
	Off        Phase = "off"     // listening is switched off in the settings
	Opening    Phase = "opening" // waiting for the devices, and often for a permission dialog
	Listening  Phase = "listening"
	Recording  Phase = "recording"
	WrappingUp Phase = "wrapping up" // it has gone quiet, and this may be the end
	Paused     Phase = "paused"
	Broken     Phase = "broken"
)

// Status is the snapshot the window polls.
type Status struct {
	Phase   Phase  `json:"phase"`
	Kind    Kind   `json:"kind"`
	Elapsed int    `json:"elapsed"` // seconds of the current recording
	Quiet   int    `json:"quiet"`   // seconds since anybody last spoke
	System  bool   `json:"system"`  // is the other side being captured at all
	Problem string `json:"problem"`
}

// Done is what the app does with a finished recording: in practice, add it to
// the library queue.
type Done func(path string, kind Kind, started time.Time)

// stamp is the file name, which is also how a recording is found again on disk.
// Dashes rather than colons: a colon in a POSIX name shows up as a slash in
// Finder, and these files are meant to be found by a person.
const stamp = "2006-01-02 15-04"

// partial is the suffix a recording wears while it is still being written, so
// that a file killed mid-write can never be mistaken for a finished one.
const partial = ".part"

// Recorder is the whole loop: audio in, finished recordings out.
type Recorder struct {
	dir      string
	model    string
	ring     *Ring
	detector *Detector
	preroll  time.Duration
	done     Done

	mic, sys *Ears
	scribe   *Scribe

	// Quiet frames are held back rather than written: if the talking resumes
	// they are flushed, and if the recording ends they are dropped. Without
	// this every meeting ended with the minutes of silence the detector needed
	// to be sure it was over, and Whisper filled them with invented text.
	pending [][]int16

	// Everything below is read by the window from another goroutine.
	mu     sync.Mutex
	writer *Writer
	kind   Kind
	begun  time.Time
	paused bool
	want   *bool // Record now / Stop, waiting to be applied by the capture loop
	snap   Status
}

// Write turns an utterance into words. Handed in rather than imported so that
// this package still knows nothing about Whisper; nil switches the live
// transcript off.
type Write func([]float32) (string, error)

// New wires the pieces from the settings. Nothing is opened yet — Run does
// that, because opening the microphone can block on a person answering a
// permission dialog.
func New(recordings, vadModel string, cfg home.Listen, done Done, write Write) *Recorder {
	return &Recorder{
		dir:     recordings,
		model:   vadModel,
		ring:    NewRing(cfg.Ring.Duration),
		preroll: cfg.Preroll.Duration,
		done:    done,
		detector: NewTunedDetector(Tuning{
			StartSpeech:  cfg.StartSpeech.Duration,
			QuietMeeting: cfg.QuietEnds.Duration,
			QuietNote:    cfg.QuietEnds.Duration / 3,
		}),
		scribe: NewScribe(write),
		snap:   Status{Phase: Opening},
	}
}

// Said is the live transcript of the recording in progress.
func (r *Recorder) Said() []Line { return r.scribe.Said() }

// Recover clears recordings that were still being written when the app last
// died. Their WAV headers were never patched, so they hold no readable audio.
func Recover(dir string) {
	stale, _ := filepath.Glob(filepath.Join(dir, "*"+partial))
	for _, path := range stale {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		slog.Warn("discarding a recording that was interrupted mid-write",
			"file", filepath.Base(path),
			"audio", (time.Duration(info.Size()/4) * time.Second / audio.SampleRate).Round(time.Second))
		_ = os.Remove(path)
	}
}

// Run opens the devices and records until the context is cancelled. A recording
// in progress is finished rather than abandoned.
func (r *Recorder) Run(ctx context.Context) error {
	var err error
	if r.mic, err = Listen(r.model); err != nil {
		r.fail(err)
		return err
	}
	defer r.mic.Close()
	if r.sys, err = Listen(r.model); err != nil {
		r.fail(err)
		return err
	}
	defer r.sys.Close()

	stream, err := audio.Open(ctx)
	if err != nil {
		r.fail(err)
		return err
	}
	defer stream.Close()
	defer r.finish("shutting down")

	r.mu.Lock()
	r.snap = Status{Phase: Listening, System: stream.SystemAudio}
	system := stream.SystemAudio
	r.mu.Unlock()
	slog.Info("listening", "system_audio", system)

	left := make([]int16, audio.FrameSize)
	right := make([]int16, audio.FrameSize)

	for {
		select {
		case <-ctx.Done():
			return nil
		case frame, ok := <-stream.Frames():
			if !ok {
				return nil
			}
			if r.Paused() {
				continue
			}
			for i := range audio.FrameSize {
				left[i], right[i] = frame[2*i], frame[2*i+1]
			}
			if err := r.step(frame, left, right, system); err != nil {
				r.fail(err)
				return err
			}
		}
	}
}

// step is one frame: remember it, ask each channel whether anybody is talking,
// and act on whatever the detector makes of it.
func (r *Recorder) step(frame, left, right []int16, system bool) error {
	// The window only ever leaves a note; the detector is touched here and
	// nowhere else.
	r.mu.Lock()
	if want := r.want; want != nil {
		r.want = nil
		r.mu.Unlock()
		r.detector.Force(*want)
	} else {
		r.mu.Unlock()
	}

	speaking, mine, err := r.mic.Speaking(left)
	if err != nil {
		return err
	}
	others, theirs, err := r.sys.Speaking(right)
	if err != nil {
		return err
	}
	if r.writer != nil {
		// Only while something is being recorded: transcribing the room at
		// large, all day, would be a different and much ruder application.
		for _, u := range mine {
			r.scribe.Hear("you", u)
		}
		for _, u := range theirs {
			r.scribe.Hear("them", u)
		}
	}
	r.ring.Add(frame, speaking || others)

	switch r.detector.Feed(speaking, others) {
	case Started:
		if err := r.begin(); err != nil {
			return err
		}
	case Rolled:
		if err := r.flush(nil); err != nil {
			return err
		}
		if err := r.finish("hourly split"); err != nil {
			return err
		}
		// The next file starts now, not in the past: the preroll belongs to the
		// beginning of the meeting, and it is already in the previous file.
		if err := r.open(0); err != nil {
			return err
		}
	case Ended:
		if err := r.finish("quiet"); err != nil {
			return err
		}
	}
	r.observe(system)

	// A note that turns out to have somebody on the other end was a meeting all
	// along, and the file has to follow.
	if _, kind := r.detector.Recording(); r.writer != nil && kind != r.kind {
		slog.Info("this is a conversation after all", "was", r.kind, "now", kind)
		r.mu.Lock()
		r.kind = kind
		r.mu.Unlock()
	}
	if r.writer == nil {
		return nil
	}
	if !speaking && !others {
		r.pending = append(r.pending, append([]int16(nil), frame...))
		return nil
	}
	return r.flush(frame)
}

// flush writes everything that was held back, then the frame that broke the
// silence. A pause inside a meeting belongs in the recording; the silence after
// the meeting does not, and only hindsight tells them apart.
func (r *Recorder) flush(frame []int16) error {
	for _, held := range r.pending {
		if err := r.writer.Write(held); err != nil {
			return err
		}
	}
	r.pending = r.pending[:0]
	if frame == nil {
		return nil
	}
	return r.writer.Write(frame)
}

// begin opens a recording and pours the ring into it, so that the meeting
// starts where it actually started rather than where it was noticed.
func (r *Recorder) begin() error {
	// The ring is asked first, because the file name carries the start time and
	// the ring may hold less than the preroll asks for — a meeting in the first
	// minutes after launch, most obviously.
	replay := r.ring.ReplaySpeech(r.preroll)
	if err := r.open(time.Duration(len(replay)) * frameDuration); err != nil {
		return err
	}
	for _, frame := range replay {
		if err := r.writer.Write(frame); err != nil {
			return err
		}
	}
	r.scribe.Start()
	slog.Info("recording started", "kind", r.kind,
		"replayed", (time.Duration(len(replay)) * frameDuration).Round(time.Second))
	return nil
}

func (r *Recorder) open(back time.Duration) error {
	_, kind := r.detector.Recording()
	begun := time.Now().Add(-back)

	writer, err := Create(r.path(kind, begun) + partial)
	if err != nil {
		return fmt.Errorf("open a recording: %w", err)
	}
	r.mu.Lock()
	r.writer, r.kind, r.begun = writer, kind, begun
	r.mu.Unlock()
	return nil
}

func (r *Recorder) path(kind Kind, at time.Time) string {
	return filepath.Join(r.dir, fmt.Sprintf("%s %s.wav", kind, at.Format(stamp)))
}

// finish closes the current recording and hands it to the library. The kind may
// have changed since it was opened, so the file is renamed to match first.
func (r *Recorder) finish(why string) error {
	r.mu.Lock()
	writer, kind, begun := r.writer, r.kind, r.begun
	r.writer = nil
	r.mu.Unlock()
	if writer == nil {
		return nil
	}

	// Whatever is still held back is the silence that ended the recording. It
	// is dropped here, which is the whole point of holding it.
	dropped := time.Duration(len(r.pending)) * frameDuration
	r.pending = r.pending[:0]

	r.scribe.Stop()
	held := writer.Duration()
	if err := writer.Close(); err != nil {
		return err
	}
	final := r.path(kind, begun)
	if err := writer.Rename(final); err != nil {
		return err
	}
	slog.Info("recording finished", "kind", kind,
		"length", held.Round(time.Second),
		"trimmed", dropped.Round(time.Second), "why", why)

	// A recording too short to hold a sentence is a false start, not a meeting.
	if held < 5*time.Second {
		slog.Info("too short to keep", "length", held.Round(time.Second))
		_ = os.Remove(final)
		return nil
	}
	// The VAD is recurrent, so the end of one recording would otherwise colour
	// the start of the next.
	r.mic.Forget()
	r.sys.Forget()
	r.done(final, kind, begun)
	return nil
}

// observe takes a snapshot for the window. The detector belongs to the capture
// loop and is never read from another goroutine.
func (r *Recorder) observe(system bool) {
	on, kind := r.detector.Recording()
	status := Status{Phase: Listening, System: system}
	if on {
		status = Status{
			Phase:   Recording,
			Kind:    kind,
			System:  system,
			Elapsed: int(r.detector.Elapsed().Seconds()),
			Quiet:   int(r.detector.Quiet().Seconds()),
		}
		if r.detector.Quiet() > 10*time.Second {
			status.Phase = WrappingUp
		}
	}
	r.mu.Lock()
	if r.paused {
		status = Status{Phase: Paused, System: system}
	}
	r.snap = status
	r.mu.Unlock()
}

func (r *Recorder) fail(err error) {
	slog.Error("listening stopped", "err", err)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap = Status{Phase: Broken, Problem: err.Error()}
}

// Status is what the window shows.
func (r *Recorder) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap
}

// Toggle is the Record now / Stop button. A forced recording ignores silence,
// so an in-person meeting with long pauses is not cut short.
func (r *Recorder) Toggle() {
	on := !r.Recording()
	r.mu.Lock()
	r.want = &on
	r.mu.Unlock()
}

// Recording reports whether audio is being kept right now.
func (r *Recorder) Recording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap.Phase == Recording || r.snap.Phase == WrappingUp
}

// Pause stops capture without stopping the app. Anything being recorded is
// finished first, so pausing never truncates a file mid-write.
func (r *Recorder) Pause(on bool) {
	r.mu.Lock()
	already := r.paused == on
	r.paused = on
	r.mu.Unlock()
	if already {
		return
	}
	if on {
		_ = r.finish("paused")
	}
	r.mu.Lock()
	if on {
		r.snap.Phase = Paused
	} else {
		r.snap.Phase = Listening
	}
	r.mu.Unlock()
	slog.Info("capture", "paused", on)
}

func (r *Recorder) Paused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}
