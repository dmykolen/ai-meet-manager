// Package service is what the interface can call.
//
// Wails binds these methods to the frontend directly, with generated types, so
// there is no HTTP layer, no JSON hand-rolling and no port to collide with
// anything. Every method here is something a person does: open the library,
// read a meeting, ask a question, rename a speaker.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/home"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/library"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/listen"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/store"
)

// Meetings is the service the interface talks to.
type Meetings struct {
	db      *store.DB
	lib     *library.Library
	dir     string
	config  home.Config
	started time.Time

	// The listener arrives once the models are on disk, which on a fresh
	// install is several minutes after the window opens.
	ears *listen.Recorder
}

// Listener hands over the always-on recorder when it is ready.
func (m *Meetings) Listener(r *listen.Recorder) {
	m.ears = r
	r.Pause(!m.config.Listen.Enabled)
}

// Listening is the state of the always-on recorder, polled by the window: a
// wait with no name is a bug, and this is the longest wait in the app.
func (m *Meetings) Listening() listen.Status {
	if m.ears == nil {
		return listen.Status{Phase: listen.Opening}
	}
	if !m.config.Listen.Enabled {
		return listen.Status{Phase: listen.Off}
	}
	return m.ears.Status()
}

// Summaries reports whether there is a key to summarise and answer with. The
// window asks so that it can say the feature is off rather than look broken.
func (m *Meetings) Summaries() bool { return m.config.OpenAIKey != "" }

// Record is the button: start a recording now, or stop the one running. A
// forced recording ignores silence, which is what makes it useful for a meeting
// in a room where nothing comes out of the speakers.
func (m *Meetings) Record() {
	if m.ears != nil {
		m.ears.Toggle()
	}
}

func New(db *store.DB, lib *library.Library, dir string, cfg home.Config) *Meetings {
	return &Meetings{db: db, lib: lib, dir: dir, config: cfg, started: time.Now()}
}

// Recent is the Library screen: what has been recorded, newest first.
func (m *Meetings) Recent(limit int) ([]store.Recording, error) {
	found, err := m.db.Recent(limit)
	if err != nil {
		return nil, err
	}
	if found == nil {
		// An empty list rather than null: the interface should render an empty
		// state, not crash on a missing array.
		return []store.Recording{}, nil
	}
	return found, nil
}

// Meeting is one recording with its transcript.
type Meeting struct {
	store.Recording
	Turns []store.Turn `json:"transcript"`
}

// Open is a whole meeting, ready to read.
func (m *Meetings) Open(id int64) (*Meeting, error) {
	r, err := m.db.Get(id)
	if err != nil {
		return nil, fmt.Errorf("recording %d: %w", id, err)
	}
	turns, err := m.db.Turns(id)
	if err != nil {
		return nil, err
	}
	if turns == nil {
		turns = []store.Turn{}
	}
	return &Meeting{Recording: *r, Turns: turns}, nil
}

// Search finds passages across every transcript.
func (m *Meetings) Search(query string) ([]store.Hit, error) {
	hits, err := m.lib.Find(context.Background(), query, 40)
	if err != nil {
		return nil, err
	}
	if hits == nil {
		return []store.Hit{}, nil
	}
	return hits, nil
}

// Answer is what Ask returns: the reply and where it came from, so that nothing
// has to be taken on trust.
type Answer struct {
	Text    string      `json:"text"`
	Sources []store.Hit `json:"sources"`
}

// Ask answers a question from the transcripts.
func (m *Meetings) Ask(question string) (*Answer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	text, hits, err := m.lib.Ask(ctx, question)
	if err != nil {
		return nil, err
	}
	return &Answer{Text: text, Sources: hits}, nil
}

// Rename changes a speaker's label throughout one meeting. The commonest edit
// there is: SPEAKER_00 becomes Olena.
// Renaming a speaker is also how the app learns a voice. That is the whole
// point of doing it here rather than in a settings screen nobody visits: you
// name somebody once, in the meeting where you recognised them, and from then
// on they arrive already named.
func (m *Meetings) Rename(id int64, from, to string) error {
	print := m.db.VoiceIn(id, from)
	if err := m.db.Rename(id, from, to); err != nil {
		return err
	}
	if len(print) == 0 {
		// Too little of them to be worth a signature — the rename still stands
		// for this meeting, it just teaches nothing.
		return nil
	}
	if err := m.db.Remember(to, print); err != nil {
		slog.Warn("renamed, but could not learn the voice", "name", to, "err", err)
	}
	return nil
}

// ThisIsMe puts a name to the person holding the laptop.
//
// Everything the microphone hears is already one speaker, called "You" — so
// this is the one enrolment the app can be certain about, and doing it once
// makes every note and every one of your own turns carry your name from then
// on. Any recording will do; the most recent one with a voiceprint is used.
func (m *Meetings) ThisIsMe(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("a name is needed")
	}
	recent, err := m.db.Recent(200)
	if err != nil {
		return "", err
	}
	taught := 0
	for _, r := range recent {
		print := m.db.VoiceIn(r.ID, library.Me)
		if len(print) == 0 {
			continue
		}
		if err := m.db.Remember(name, print); err != nil {
			return "", err
		}
		if err := m.db.Rename(r.ID, library.Me, name); err != nil {
			return "", err
		}
		if taught++; taught >= store.Keep {
			break
		}
	}
	if taught == 0 {
		return "", errors.New("nothing recorded yet has enough of your voice in it — record something first")
	}
	return fmt.Sprintf("Learnt your voice from %d %s. Your turns are now named %s.",
		taught, plural(taught, "recording"), name), nil
}

// People is everybody the app can now recognise by voice.
func (m *Meetings) People() ([]store.Person, error) {
	people, err := m.db.People()
	if err != nil || people == nil {
		return []store.Person{}, err
	}
	return people, nil
}

// Forget drops a person, which is how a name given to the wrong voice is undone.
func (m *Meetings) Forget(name string) error { return m.db.Forget(name) }

// Analytics is the shape of a meeting: who held the floor, how fast, who asked
// the questions. Measured from the rows, so it follows a rename immediately.
func (m *Meetings) Analytics(id int64) (*store.Analytics, error) {
	r, err := m.db.Get(id)
	if err != nil {
		return nil, err
	}
	turns, err := m.db.Turns(id)
	if err != nil {
		return nil, err
	}
	a := store.Analyse(turns, r.Duration)
	return &a, nil
}

// Reindex embeds every transcript that has no vectors yet, which is how
// semantic search reaches the meetings recorded before a key was added.
func (m *Meetings) Reindex() (string, error) {
	n, err := m.lib.Reindex(context.Background())
	if err != nil {
		return "", err
	}
	with, without := m.db.Indexed()
	return fmt.Sprintf("Indexed %d %s. %d passages searchable by meaning, %d by keyword only.",
		n, plural(n, "recording"), with, without), nil
}

// Tidy deletes audio older than the setting, now. The same sweep runs daily on
// its own; this is the button for somebody who has just noticed the folder is
// twelve gigabytes.
func (m *Meetings) Tidy() (string, error) {
	gone, freed, err := library.Sweep(m.db, filepath.Join(m.dir, "recordings"), m.config.Keep.AudioDays)
	switch {
	case err != nil:
		return "", err
	case m.config.Keep.AudioDays <= 0:
		return "Nothing was deleted: audio is set to be kept for ever.", nil
	case gone == 0:
		return "Nothing to delete — no audio is older than that yet.", nil
	}
	return fmt.Sprintf("Deleted %d %s, %d MB. The transcripts are untouched.",
		gone, plural(gone, "recording"), freed/(1<<20)), nil
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// Brief is the first screen: what happened in the last few days, what was
// settled, what is still owed, and what keeps being asked without an answer.
func (m *Meetings) Brief(days int) (*store.Briefing, error) { return m.db.Brief(days) }

// Live is the transcript of the meeting happening right now, written utterance
// by utterance while it runs.
func (m *Meetings) Live() []listen.Line {
	if m.ears == nil {
		return []listen.Line{}
	}
	if said := m.ears.Said(); said != nil {
		return said
	}
	return []listen.Line{}
}

// Again transcribes a recording from scratch — after the speakers were named,
// after the language was set, or after anything in the app that reads audio got
// better.
func (m *Meetings) Again(id int64) error { return m.lib.Again(id) }

// Summarise reads a meeting again — after a key was added, or after the
// speakers were given names and the summary should use them.
func (m *Meetings) Summarise(id int64) error {
	return m.lib.Summarise(context.Background(), id)
}

// SaveNote keeps what somebody typed against a meeting.
func (m *Meetings) SaveNote(id int64, note string) error {
	return m.db.SaveNote(id, note)
}

// Delete forgets a meeting and its audio.
func (m *Meetings) Delete(id int64) error {
	return m.lib.Delete(id)
}

// Import takes a file from anywhere, copies it into the recordings folder and
// queues it. Copying rather than referencing: a library that breaks when
// somebody tidies their Downloads folder is not a library.
func (m *Meetings) Import(path string) (*store.Recording, error) {
	source, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer source.Close()

	name := fmt.Sprintf("%s-%s", time.Now().Format("2006-01-02T15-04-05"), filepath.Base(path))
	destination := filepath.Join(home.Recordings(m.dir), name)
	target, err := os.Create(destination)
	if err != nil {
		return nil, err
	}
	if _, err := target.ReadFrom(source); err != nil {
		target.Close()
		os.Remove(destination)
		return nil, err
	}
	target.Close()

	r, err := m.lib.Add(store.Meeting, name, time.Now(), filepath.Base(path))
	return &r, err
}

// Settings is what the settings screen reads and writes.
type Settings struct {
	Language    string `json:"language"`
	OpenAIKey   string `json:"openaiKey"`
	OpenAIModel string `json:"openaiModel"`
	Summarise   bool   `json:"summarise"`
	Density     string `json:"density"`

	Listening   bool `json:"listening"`
	StartSpeech int  `json:"startSpeech"` // seconds of talking before it records
	QuietEnds   int  `json:"quietEnds"`   // seconds of silence that end it
	Preroll     int  `json:"preroll"`     // seconds it reaches back when it starts

	KeepAudioDays int `json:"keepAudioDays"` // 0 keeps recordings for ever

	Folder string `json:"folder"`
}

func (m *Meetings) Settings() Settings {
	return Settings{
		Language:      m.config.Language,
		OpenAIKey:     m.config.OpenAIKey,
		OpenAIModel:   m.config.OpenAIModel,
		Summarise:     m.config.Summarise,
		Density:       m.config.Density,
		Listening:     m.config.Listen.Enabled,
		StartSpeech:   int(m.config.Listen.StartSpeech.Seconds()),
		QuietEnds:     int(m.config.Listen.QuietEnds.Seconds()),
		Preroll:       int(m.config.Listen.Preroll.Seconds()),
		KeepAudioDays: m.config.Keep.AudioDays,
		Folder:        m.dir,
	}
}

// SaveSettings writes them back. The key is the only thing here worth guarding,
// and it goes to a file in the user's own folder and nowhere else.
func (m *Meetings) SaveSettings(s Settings) error {
	m.config.Language = s.Language
	m.config.OpenAIKey = s.OpenAIKey
	m.config.OpenAIModel = s.OpenAIModel
	m.config.Summarise = s.Summarise
	m.config.Density = s.Density
	m.config.Listen.Enabled = s.Listening
	m.config.Keep.AudioDays = s.KeepAudioDays
	// Switching listening off pauses the recorder rather than tearing it down:
	// the devices took a permission dialog to open, and the setting is a thing
	// people flick back and forth.
	if m.ears != nil {
		m.ears.Pause(!s.Listening)
	}

	// Guarded rather than trusted: a zero here would make the listener start on
	// the first breath and never stop, and the settings screen is one typo away
	// from sending a zero.
	m.config.Listen.StartSpeech = home.Duration{Duration: seconds(s.StartSpeech, 20, 5, 300)}
	m.config.Listen.QuietEnds = home.Duration{Duration: seconds(s.QuietEnds, 180, 15, 1800)}
	m.config.Listen.Preroll = home.Duration{Duration: seconds(s.Preroll, 300, 0, 600)}
	if m.config.Listen.Preroll.Duration > m.config.Listen.Ring.Duration {
		m.config.Listen.Ring = m.config.Listen.Preroll
	}
	return home.Save(m.dir, m.config)
}

// seconds turns what the settings screen sent into something the listener can
// live with: a fallback when it is zero, and clamped either way.
func seconds(given, fallback, low, high int) time.Duration {
	if given <= 0 {
		given = fallback
	}
	return time.Duration(min(max(given, low), high)) * time.Second
}

// RevealFolder opens ~/MeetingTranscriber in the file manager, which is the
// answer to "where are my recordings".
func (m *Meetings) RevealFolder() error {
	command := map[string]string{"darwin": "open", "windows": "explorer"}[runtime.GOOS]
	if command == "" {
		command = "xdg-open"
	}
	return exec.Command(command, m.dir).Start()
}

// Actions is the to-do list across every meeting.
func (m *Meetings) Actions(includeDone bool) ([]store.Outstanding, error) {
	return m.db.Actions(includeDone)
}

// Tick marks one action item done, or undone.
func (m *Meetings) Tick(id int64, index int, done bool) error {
	return m.db.TickAction(id, index, done)
}

// Markdown renders a whole meeting for pasting somewhere else. Export as the
// smallest thing that could work: one string, and the interface puts it on the
// clipboard.
func (m *Meetings) Markdown(id int64) (string, error) {
	r, err := m.db.Get(id)
	if err != nil {
		return "", err
	}
	turns, err := m.db.Turns(id)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n_%s · %s_\n\n", r.Title,
		r.Started.Format("2 January 2006, 15:04"), length(r.Duration))

	if s := r.Summary; s != nil {
		if s.Overview != "" {
			fmt.Fprintf(&b, "%s\n\n", s.Overview)
		}
		section(&b, "Decided", s.Decisions)
		if len(s.ActionItems) > 0 {
			b.WriteString("## To do\n\n")
			for _, a := range s.ActionItems {
				mark := " "
				if a.Done {
					mark = "x"
				}
				fmt.Fprintf(&b, "- [%s] %s", mark, a.Task)
				if a.Owner != "" {
					fmt.Fprintf(&b, " — **%s**", a.Owner)
				}
				if a.Due != "" {
					fmt.Fprintf(&b, " (%s)", a.Due)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		section(&b, "Left open", s.OpenQuestions)
	}

	b.WriteString("## Transcript\n\n")
	for _, t := range turns {
		who := t.Speaker
		if who == "" {
			who = "—"
		}
		fmt.Fprintf(&b, "**%s** `%s` %s\n\n", who, clock(t.Start), t.Text)
	}
	return b.String(), nil
}

func section(b *strings.Builder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n", title)
	for _, l := range lines {
		fmt.Fprintf(b, "- %s\n", l)
	}
	b.WriteString("\n")
}

func clock(seconds float64) string {
	s := int(seconds)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func length(seconds float64) string {
	m := int(seconds / 60)
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d hr %d min", m/60, m%60)
}
