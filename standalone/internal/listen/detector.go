package listen

import (
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/store"
)

// Kind is what a recording turned out to be — the store's own type, because
// that is where it ends up and a second copy of two constants would be two
// copies to keep in step.
type Kind = store.Kind

const (
	Meeting = store.Meeting
	Note    = store.Note
)

// Defaults. Every one is a guess that a week of real use will correct, which is
// why they are in one place, and why Tuning lets the config move them without a
// rebuild.
const (
	StartSpeech = 20 * time.Second // this much speech...
	StartWindow = 60 * time.Second // ...within this window starts a recording

	// A blip of system audio is a notification chime, not a conversation.
	MeetingAudio = 3 * time.Second

	QuietMeeting = 3 * time.Minute // silence that ends a meeting
	QuietNote    = 60 * time.Second

	RollAfter = time.Hour     // files are cut here, without a gap
	HardCap   = 3 * time.Hour // and a session cannot outlive this
)

// Tuning is the thresholds a person may want to move. Anything zero falls back
// to the default above, so a config naming one setting does not silently reset
// the rest.
type Tuning struct {
	StartSpeech  time.Duration
	StartWindow  time.Duration
	MeetingAudio time.Duration
	QuietMeeting time.Duration
	QuietNote    time.Duration
}

func (t Tuning) withDefaults() Tuning {
	for _, f := range []struct {
		at  *time.Duration
		def time.Duration
	}{
		{&t.StartSpeech, StartSpeech},
		{&t.StartWindow, StartWindow},
		{&t.MeetingAudio, MeetingAudio},
		{&t.QuietMeeting, QuietMeeting},
		{&t.QuietNote, QuietNote},
	} {
		if *f.at <= 0 {
			*f.at = f.def
		}
	}
	return t
}

// Transition is what changed on this frame.
type Transition int

const (
	Continue Transition = iota
	Started             // open a file, replay the ring into it
	Rolled              // close this file and open the next; still recording
	Ended               // close the file; the session is over
)

// Detector decides when a recording starts, what kind it is, and when it ends.
//
// With the system channel captured the whole classifier is one question — is
// anybody else talking? — hence no process enumeration, no window titles, no
// calendar. It counts frames rather than reading a clock, so the same input
// always gives the same answer and tests never sleep.
type Detector struct {
	tuning    Tuning
	forced    bool // somebody pressed Record now
	stopping  bool // ...and has now pressed Stop
	recording bool
	kind      Kind
	held      int // frames in the current file
	total     int // frames since the session began
	quiet     int // consecutive silent frames

	window   []state // the rolling StartWindow
	at       int
	speaking int  // frames of the window with speech on either channel
	others   int  // frames of the window with speech on the system channel
	company  bool // a second voice was heard on the microphone alone
}

type state struct{ any, sys bool }

// NewDetector uses the defaults; NewTunedDetector takes them from the config.
func NewDetector() *Detector { return NewTunedDetector(Tuning{}) }

func NewTunedDetector(tuning Tuning) *Detector {
	t := tuning.withDefaults()
	return &Detector{tuning: t, window: make([]state, frames(t.StartWindow))}
}

// Feed advances the machine by one frame. mic and sys are whether the VAD heard
// speech on each channel.
func (d *Detector) Feed(mic, sys bool) Transition {
	d.remember(state{any: mic || sys, sys: sys})

	if !d.recording {
		if !d.forced && d.speaking < frames(d.tuning.StartSpeech) {
			return Continue
		}
		d.recording, d.held, d.total, d.quiet = true, 0, 0, 0
		// The window decides, not this frame. Checking only the frame that
		// crossed the threshold would file a meeting as a note whenever its
		// first twenty seconds were you saying hello.
		d.kind = Note
		if d.others >= frames(d.tuning.MeetingAudio) {
			d.kind = Meeting
		}
		if d.forced && d.others < frames(d.tuning.MeetingAudio) {
			// Pressing the button is not evidence of a second person. Somebody
			// dictating a thought gets a note; if the speakers then start
			// talking, the rule below promotes it to a meeting on its own.
			d.kind = Note
		}
		return Started
	}

	d.held++
	d.total++
	// A note that acquires a second voice was a meeting all along — whether the
	// second voice arrived on the system channel or was heard in the room.
	if (sys || d.company) && d.kind == Note {
		d.kind = Meeting
	}
	if mic || sys {
		d.quiet = 0
	} else {
		d.quiet++
	}

	switch {
	case d.stopping:
		// Every transition comes out of Feed, including the ones a person asks
		// for, so that the recorder has exactly one place to close a file.
		d.stopping, d.recording = false, false
		d.clearWindow()
		return Ended
	// A forced recording ignores silence: somebody pressed the button, and the
	// pauses in an in-person meeting are none of the detector's business.
	case !d.forced && d.quiet >= frames(d.quietEnough()), d.total >= frames(HardCap):
		d.recording = false
		d.clearWindow()
		return Ended
	case d.held >= frames(RollAfter):
		// Rolling rather than stopping: an hour-long file is a bounded loss if
		// something goes wrong, but making the meeting earn its twenty seconds
		// of speech again would punch a hole in it every hour.
		d.held = 0
		return Rolled
	}
	return Continue
}

func (d *Detector) quietEnough() time.Duration {
	if d.kind == Meeting {
		return d.tuning.QuietMeeting
	}
	return d.tuning.QuietNote
}

// Recording reports whether audio is being kept right now, and as what.
func (d *Detector) Recording() (bool, Kind) { return d.recording, d.kind }

// Force records until released — for the meeting the detector cannot see:
// everybody in one room, nothing through the speakers, long silences.
//
// Turning it off stops whatever is recording, however it began. Stopping only
// forced recordings made the button do nothing for every meeting the app had
// started itself, which is almost all of them.
//
// Sets a flag only; Feed starts and stops on its own goroutine.
// Company says a voice has been heard that belongs to nobody heard before, so
// there is more than one person in the room. It is the microphone-only stand-in
// for speech on the system channel, and it only ever promotes: a recording that
// has become a meeting does not go back to being a note because somebody went
// quiet.
func (d *Detector) Company() { d.company = true }

func (d *Detector) Force(on bool) {
	if !on && d.recording {
		d.stopping = true
	}
	d.forced = on
}

// Forced reports whether the current recording was asked for by a person.
func (d *Detector) Forced() bool { return d.forced }

// Held is how long the current file has been running, Elapsed the whole
// session, and Quiet how long it has heard nothing — which is what the tray
// shows as "wrapping up".
func (d *Detector) Held() time.Duration    { return time.Duration(d.held) * frameDuration }
func (d *Detector) Elapsed() time.Duration { return time.Duration(d.total) * frameDuration }
func (d *Detector) Quiet() time.Duration   { return time.Duration(d.quiet) * frameDuration }

func frames(d time.Duration) int { return int(d / frameDuration) }

// remember slides the window along by one frame, keeping both counts current so
// that nothing has to walk the window to ask a question of it.
func (d *Detector) remember(s state) {
	old := d.window[d.at]
	if old.any {
		d.speaking--
	}
	if old.sys {
		d.others--
	}
	if s.any {
		d.speaking++
	}
	if s.sys {
		d.others++
	}
	d.window[d.at] = s
	d.at = (d.at + 1) % len(d.window)
}

// clearWindow stops the silence that ended one recording from being carried
// into the decision about the next.
func (d *Detector) clearWindow() {
	clear(d.window)
	d.speaking, d.others, d.at, d.company = 0, 0, 0, false
}
