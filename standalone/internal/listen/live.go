package listen

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Scribe writes the meeting down while it is still happening.
//
// The audio is already being cut into utterances — the detector has to know
// when somebody stopped talking anyway — so the only thing missing was somebody
// to hand them to. Each one goes through Whisper as it finishes and appears in
// the window a second or two later.
//
// Deliberately not the transcript. Whisper on a four-second fragment is worse
// than Whisper on the whole meeting: it has no context, it will not spell a
// name it has not heard yet, and it cannot be diarized. This is for reading
// along and catching what somebody just said; the real transcript is written
// afterwards from the file, and replaces it.
type Scribe struct {
	transcribe func([]float32) (string, error)

	mu      sync.Mutex
	lines   []Line
	pending int
	started time.Time
}

// A Line is one utterance, written down.
type Line struct {
	At   int    `json:"at"`  // seconds into the recording
	Who  string `json:"who"` // "you" or "them" — which channel it came from
	Text string `json:"text"`
}

// Lines is how much of the live transcript is kept in memory. A long meeting
// scrolls; nobody reads back an hour of it in the sidebar, and the file has
// every word regardless.
const Lines = 400

// NewScribe takes the one function it needs from the engine, so that this
// package never learns that Whisper exists.
func NewScribe(transcribe func([]float32) (string, error)) *Scribe {
	return &Scribe{transcribe: transcribe}
}

// Start clears the board for a new recording.
func (s *Scribe) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines, s.pending, s.started = nil, 0, time.Now()
}

// Hear queues an utterance. It returns immediately: this is called from the
// capture loop, which must not wait for a model.
//
// One in flight at a time. Whisper's encoder runs a thirty-second window
// whatever it is fed, so two at once is not twice as fast — it is two things
// queueing on the same cores while the microphone waits for neither.
func (s *Scribe) Hear(who string, u Utterance) {
	s.mu.Lock()
	if s.pending > 0 || s.transcribe == nil || s.started.IsZero() {
		s.mu.Unlock()
		return
	}
	s.pending++
	at := int(time.Since(s.started).Seconds())
	s.mu.Unlock()

	go func() {
		text, err := s.transcribe(u.Samples)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.pending--
		if err != nil {
			slog.Debug("live transcription skipped", "err", err)
			return
		}
		if text = strings.TrimSpace(text); text == "" {
			return
		}
		s.lines = append(s.lines, Line{At: at, Who: who, Text: text})
		if len(s.lines) > Lines {
			s.lines = s.lines[len(s.lines)-Lines:]
		}
	}()
}

// Said is the live transcript so far, oldest first.
func (s *Scribe) Said() []Line {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Line(nil), s.lines...)
}

// Stop puts the board away. The finished recording is transcribed properly from
// the file, and keeping the rough version around would only invite somebody to
// read it instead.
func (s *Scribe) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines, s.started = nil, time.Time{}
}
