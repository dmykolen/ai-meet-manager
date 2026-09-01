package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/source"
	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/vad"
)

// Spool is where a finished recording is left for the uploader. Kept as an
// interface so that this package never has to know the server exists.
type Spool interface {
	Path(kind string, at time.Time) string
	Wake()
}

// Phase is what the daemon is doing, for the tray. Every wait is a visible
// state; a pause with no name is a bug.
type Phase string

const (
	// Opening and NeedsPermission exist because the menu bar item has to appear
	// before the microphone does. Blocking on the devices first meant that a
	// missing permission showed as no icon at all — the one state in which a
	// person most needs to be told something.
	Opening         Phase = "opening"
	NeedsPermission Phase = "needs permission"

	Listening  Phase = "listening"
	Recording  Phase = "recording"
	WrappingUp Phase = "wrapping up"
	Paused     Phase = "paused"
)

// Status is a snapshot for the tray.
type Status struct {
	Phase   Phase
	Kind    Kind
	Elapsed time.Duration
	Quiet   time.Duration
}

// Recorder is the daemon's whole loop: audio in, files in the spool out.
type Recorder struct {
	ring     *Ring
	detector *Detector
	mic, sys *vad.Detector
	spool    Spool

	// Everything below is read by the tray from another goroutine, so it is
	// only ever touched under mu — including the snapshot, because the detector
	// itself belongs to the capture loop.
	// Quiet frames are held back rather than written: if the talking resumes they
	// are flushed, and if the recording ends they are dropped. Without this every
	// meeting ended with the three minutes of silence the detector needed to be
	// sure it was over — measured at 24 minutes on one real recording — and
	// Whisper filled it with "Продолжение следует..." fourteen times over.
	pending [][]int16

	mu      sync.Mutex
	writer  *Writer
	kind    Kind
	begun   time.Time
	paused  bool
	want    *bool // Record now / Stop, waiting to be applied by the capture loop
	snap    Status
	preroll time.Duration
}

// Recover clears recordings that were still being written when the daemon last
// died. Their WAV headers were never patched, so they hold no readable audio —
// but they should be reported rather than left to puzzle somebody later.
func Recover(dir string) {
	stale, _ := filepath.Glob(filepath.Join(dir, "*"+partial))
	for _, path := range stale {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		slog.Warn("discarding a recording that was interrupted mid-write",
			"file", filepath.Base(path),
			"audio", (time.Duration(info.Size()/4) * time.Second / source.SampleRate).Round(time.Second))
		_ = os.Remove(path)
	}
}

// NewRecorder wires the pieces. preroll is how far into the past a recording
// reaches when it starts.
func NewRecorder(spool Spool, ring, preroll time.Duration, tuning Tuning) (*Recorder, error) {
	mic, err := vad.New(source.SampleRate)
	if err != nil {
		return nil, err
	}
	sys, err := vad.New(source.SampleRate)
	if err != nil {
		mic.Close()
		return nil, err
	}
	return &Recorder{
		ring:     NewRing(ring),
		detector: NewTunedDetector(tuning),
		mic:      mic,
		sys:      sys,
		spool:    spool,
		preroll:  preroll,
		// Opening, not Listening: the devices are not up yet, and claiming to
		// listen before they are would be the first lie the tray tells.
		snap: Status{Phase: Opening},
	}, nil
}

// Run consumes frames until the stream ends or the context is cancelled. A
// recording in progress is finished rather than abandoned.
func (r *Recorder) Run(ctx context.Context, frames <-chan []int16) error {
	defer r.mic.Close()
	defer r.sys.Close()
	defer r.finish("shutting down")

	left := make([]int16, source.FrameSize)
	right := make([]int16, source.FrameSize)

	for {
		select {
		case <-ctx.Done():
			return nil
		case frame, ok := <-frames:
			if !ok {
				return nil
			}
			if r.Paused() {
				continue
			}
			for i := range source.FrameSize {
				left[i], right[i] = frame[2*i], frame[2*i+1]
			}
			if err := r.step(frame, left, right); err != nil {
				return err
			}
		}
	}
}

// step is one frame: remember it, ask the VAD about each channel, and act on
// whatever the detector makes of it.
func (r *Recorder) step(frame, left, right []int16) error {
	// The tray runs on its own goroutine and only leaves a note; the detector is
	// touched here and nowhere else.
	r.mu.Lock()
	if want := r.want; want != nil {
		r.want = nil
		r.mu.Unlock()
		r.detector.Force(*want)
	} else {
		r.mu.Unlock()
	}

	speaking, err := r.mic.IsSpeech(left)
	if err != nil {
		return err
	}
	others, err := r.sys.IsSpeech(right)
	if err != nil {
		return err
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
	r.observe()

	// The kind can change mid-recording, when a note turns out to have somebody
	// on the other end. The file has to follow.
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
		// Held, not written. Bounded by the quiet period that ends a recording,
		// so it can never grow beyond a few minutes of frames.
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
	// The ring is asked first, because the name of the file carries its start
	// time and the ring may hold less than the preroll asks for — a meeting in
	// the first minutes after boot, most obviously. Naming it five minutes ago
	// when only fifty seconds were replayed would be a lie in the Library.
	replay := r.ring.ReplaySpeech(r.preroll)
	if err := r.open(time.Duration(len(replay)) * frameDuration); err != nil {
		return err
	}
	for _, frame := range replay {
		if err := r.writer.Write(frame); err != nil {
			return err
		}
	}
	slog.Info("recording started", "kind", r.kind,
		"replayed", (time.Duration(len(replay)) * frameDuration).Round(time.Second))
	return nil
}

// partial is the suffix a recording wears while it is still being written. The
// uploader only looks at *.wav, so an unfinished file is invisible to it by
// construction rather than by timing — a half-written meeting can never be sent,
// even if the daemon is killed in the middle of one.
const partial = ".part"

func (r *Recorder) open(back time.Duration) error {
	_, kind := r.detector.Recording()
	begun := time.Now().Add(-back)

	writer, err := Create(r.spool.Path(string(kind), begun) + partial)
	if err != nil {
		return fmt.Errorf("open a recording: %w", err)
	}
	r.mu.Lock()
	r.writer, r.kind, r.begun = writer, kind, begun
	r.mu.Unlock()
	return nil
}

// finish closes the current recording and hands it to the uploader. The kind
// may have changed since it was opened, so the file is renamed to match before
// the uploader ever sees it.
func (r *Recorder) finish(why string) error {
	r.mu.Lock()
	writer, kind, begun := r.writer, r.kind, r.begun
	r.writer = nil
	r.mu.Unlock()
	if writer == nil {
		return nil
	}

	// Whatever is still held back is the silence that ended the recording. It is
	// dropped here, which is the whole point of holding it.
	dropped := time.Duration(len(r.pending)) * frameDuration
	r.pending = r.pending[:0]

	held := writer.Duration()
	if err := writer.Close(); err != nil {
		return err
	}
	// Dropping the suffix is what publishes the recording to the uploader, and
	// it also fixes the name when a note turned out to be a meeting.
	if err := writer.Rename(r.spool.Path(string(kind), begun)); err != nil {
		return err
	}
	slog.Info("recording finished", "kind", kind,
		"length", held.Round(time.Second),
		"trimmed", dropped.Round(time.Second), "why", why)
	r.spool.Wake()
	return nil
}

// observe takes a snapshot of the detector for the tray. The detector belongs
// to the capture loop and is never read from another goroutine.
func (r *Recorder) observe() {
	on, kind := r.detector.Recording()
	status := Status{Phase: Listening}
	if on {
		status = Status{
			Phase:   Recording,
			Kind:    kind,
			Elapsed: r.detector.Elapsed(),
			Quiet:   r.detector.Quiet(),
		}
		if status.Quiet > 10*time.Second {
			status.Phase = WrappingUp
		}
	}
	r.mu.Lock()
	if r.paused {
		status = Status{Phase: Paused}
	}
	r.snap = status
	r.mu.Unlock()
}

// Report is how the startup path tells the tray what is happening before there
// is any audio to report on.
func (r *Recorder) Report(phase Phase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap = Status{Phase: phase}
}

// Status is what the tray shows.
func (r *Recorder) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap
}

// Busy reports whether a recording is running, which is what stops the uploader
// from making the machine transcribe during a call.
func (r *Recorder) Busy() bool { return r.Recording() }

// Toggle is the Record now / Stop button. Forced recordings ignore silence, so
// an in-person meeting with long pauses is not cut short.
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

// Pause stops capture without stopping the daemon. Anything being recorded is
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
	r.observe()
	slog.Info("capture", "paused", on)
}

func (r *Recorder) Paused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}
