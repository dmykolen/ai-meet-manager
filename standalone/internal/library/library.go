// Package library is the queue: a recording arrives, and some time later it is
// a titled, searchable transcript with a summary.
//
// One worker, not a pool. Whisper and the speaker models each want every core
// they can get, and running two recordings at once makes both slower and the
// machine hot. A queue of one is the honest shape of the work.
package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/engine"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/insights"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/media"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/store"
)

// Engine is what the library needs from the models, so that tests can hand it
// something that does not take a gigabyte to load.
type Engine interface {
	Run(samples []float32) (engine.Result, error)
	Solo(samples []float32, who string) (engine.Result, error)
}

// Me is what the person holding the laptop is called until they enrol their
// own voice and it starts using their name.
const Me = "You"

// Library owns the queue.
type Library struct {
	db   *store.DB
	llm  *insights.Client
	dir  string
	wake chan struct{}

	// The engine arrives late: on a fresh install the models are still
	// downloading when the window opens, and nothing should be processed until
	// they are here.
	mu     sync.Mutex
	engine Engine
	busy   func() bool
}

// Wait tells the queue when to stand aside. It is asked before every job, and
// while it says yes the queue sleeps rather than competing with a live meeting
// for the same cores.
func (l *Library) Wait(busy func() bool) {
	l.mu.Lock()
	l.busy = busy
	l.mu.Unlock()
}

func (l *Library) waiting() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.busy != nil && l.busy()
}

func New(db *store.DB, e Engine, llm *insights.Client, recordings string) *Library {
	return &Library{db: db, engine: e, llm: llm, dir: recordings, wake: make(chan struct{}, 1)}
}

// Use hands over the engine once the models have loaded, and starts the queue
// moving on whatever accumulated while they were downloading.
func (l *Library) Use(e Engine) {
	l.mu.Lock()
	l.engine = e
	l.mu.Unlock()
	l.Wake()
}

func (l *Library) ready() Engine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.engine
}

// Add takes a file that is already in the recordings folder and queues it.
func (l *Library) Add(kind store.Kind, audio string, started time.Time, title string) (store.Recording, error) {
	if title == "" {
		title = filepath.Base(audio)
	}
	r, err := l.db.Add(store.Recording{
		Kind:    kind,
		Title:   title,
		Audio:   filepath.Base(audio),
		Started: started,
	})
	if err == nil {
		l.Wake()
	}
	return r, err
}

// Wake asks the worker to look now rather than at the next tick.
func (l *Library) Wake() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Run works through the queue until the context is cancelled.
//
// It catches whatever one recording throws, because a single unreadable file
// must not stop every recording behind it — the failure mode that leaves a
// listener quietly filling a folder nobody is processing.
func (l *Library) Run(ctx context.Context) {
	for {
		for l.ready() != nil && !l.waiting() {
			id, ok := l.next()
			if !ok {
				break
			}
			if err := l.process(ctx, id); err != nil {
				slog.Error("recording failed", "id", id, "err", err)
				_ = l.db.Fail(id, err)
			}
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		case <-time.After(30 * time.Second):
		}
	}
}

// next is the oldest recording that is not finished. Anything left half-done by
// a crash is picked up again, which is why the status is checked rather than a
// flag held in memory.
func (l *Library) next() (int64, bool) {
	recent, err := l.db.Recent(200)
	if err != nil {
		slog.Error("cannot read the queue", "err", err)
		return 0, false
	}
	for i := len(recent) - 1; i >= 0; i-- {
		switch recent[i].Status {
		case store.Done, store.Failed:
			continue
		default:
			return recent[i].ID, true
		}
	}
	return 0, false
}

// process is the whole pipeline for one recording.
func (l *Library) process(ctx context.Context, id int64) error {
	r, err := l.db.Get(id)
	if err != nil {
		return err
	}
	path := filepath.Join(l.dir, r.Audio)
	started := time.Now()

	if err := l.db.Progress(id, store.Transcribing, 0.05); err != nil {
		return err
	}
	// Folded, not averaged: without headphones the microphone also hears the
	// far side coming out of the speakers, and averaging hands the transcriber
	// a signal summed with a room recording of itself.
	samples, err := media.Voices(path)
	if err != nil {
		return fmt.Errorf("could not read the audio: %w", err)
	}
	seconds := float64(len(samples)) / media.Rate
	slog.Info("transcribing", "id", id, "minutes", seconds/60)

	// The microphone side is the person sitting here; the system side is
	// everybody else. Knowing which is which is the one thing this app has that
	// a file dropped into a generic transcriber does not, and using it is worth
	// more than any amount of tuning the clusterer.
	mic, system, stereo := media.Sides(path)
	solo := stereo && media.Loud(system) < media.Loud(mic)/64

	var read engine.Result
	if solo {
		// Nobody on the other end. Asking a clustering algorithm how many
		// people are in a recording of one person can only be wrong, and was:
		// seven minutes of one voice came back as three speakers.
		slog.Info("one voice only: the system channel is silent", "id", id)
		read, err = l.ready().Solo(samples, Me)
	} else {
		read, err = l.ready().Run(samples)
	}
	turns := read.Turns
	if stereo && !solo {
		read = mine(read, mic, system)
	}
	if !solo {
		read.Turns = settle(read.Turns)
	}
	turns = read.Turns
	if err != nil && len(turns) == 0 {
		return err
	}
	if err != nil {
		slog.Warn("speakers unavailable", "id", id, "err", err)
	}

	// Anybody the app has been told the name of before gets their name back,
	// here, before the transcript is written — so naming somebody is something
	// you do once ever rather than once per meeting.
	if people, err := l.db.People(); err != nil {
		slog.Warn("could not read the voices", "err", err)
	} else if names := store.Recognise(read.Voices, people); len(names) > 0 {
		for i := range turns {
			if name, known := names[turns[i].Speaker]; known {
				turns[i].Speaker = name
			}
		}
		for label, name := range names {
			// Another sample of a voice already known, so recognition keeps
			// improving instead of staying as good as the first meeting.
			if err := l.db.Remember(name, read.Voices[label]); err != nil {
				slog.Warn("could not file a voice", "name", name, "err", err)
			}
			read.Voices[name] = read.Voices[label]
			delete(read.Voices, label)
		}
		slog.Info("voices recognised", "id", id, "who", values(names))
	}

	// Kept under whatever label the transcript ended up showing, so that naming
	// a speaker later — which is when people actually do it — can still reach
	// the voice that said those words.
	if err := l.db.SaveVoices(id, read.Voices); err != nil {
		slog.Warn("could not keep the voices of this recording", "id", id, "err", err)
	}

	if len(turns) == 0 {
		// Not a failure: a recording of an empty room is a real thing, and it
		// should end up as a finished, empty transcript rather than an error.
		slog.Info("nothing was said", "id", id)
	}

	if err := l.db.SaveTranscript(id, "", seconds, convert(turns)); err != nil {
		return err
	}
	slog.Info("transcribed", "id", id, "rows", len(turns),
		"pace", fmt.Sprintf("%.1fx realtime", seconds/time.Since(started).Seconds()))

	l.index(ctx, id, convert(turns))
	return l.summarise(ctx, id, turns)
}

// index cuts the transcript into passages and embeds them, so that a question
// can find a passage that does not contain any of its words.
//
// The passages are written whether or not there is a key: without one they are
// still the rows keyword search reads, and Reindex fills in the vectors the day
// a key is added.
func (l *Library) index(ctx context.Context, id int64, turns []store.Turn) {
	pieces := store.Cut(id, turns)
	if len(pieces) == 0 {
		return
	}
	var vectors [][]float32
	if l.llm.Ready() {
		texts := make([]string, len(pieces))
		for i, p := range pieces {
			texts[i] = p.Text
		}
		var err error
		if vectors, err = l.llm.Embed(ctx, texts); err != nil {
			slog.Warn("indexed for keywords only", "id", id, "err", err)
		}
	}
	if err := l.db.Index(id, pieces, vectors); err != nil {
		slog.Warn("could not index the transcript", "id", id, "err", err)
	}
}

// Reindex embeds everything that has no vector yet. Run when a key appears, so
// that semantic search covers the meetings recorded before it did.
func (l *Library) Reindex(ctx context.Context) (int, error) {
	if !l.llm.Ready() {
		return 0, errors.New("indexing needs an OpenAI key, which is in Settings")
	}
	ids, err := l.db.Stale(500)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		turns, err := l.db.Turns(id)
		if err != nil {
			return 0, err
		}
		l.index(ctx, id, turns)
	}
	return len(ids), nil
}

// mine folds everything the microphone heard into one speaker.
//
// A diarizer has no idea that half of a call arrives through a codec and the
// other half through the room, so it happily splits one person into three as
// their voice changes across an hour. The channels do know. Anything markedly
// louder on the microphone than on the system tap was said by the person
// sitting here, whatever the clusterer decided — and that also holds when the
// speakers are on and the room echoes back, because a remote voice is always
// loudest on the side it arrived from.
func mine(read engine.Result, mic, system []float32) engine.Result {
	loudest, best := "", 0.0
	for i, t := range read.Turns {
		a, b := int(t.Start*media.Rate), int(t.End*media.Rate)
		if a < 0 || b > len(mic) || b <= a {
			continue
		}
		if media.Loud(mic[a:b]) > 3*media.Loud(system[a:b]) {
			if length := t.End - t.Start; length > best {
				loudest, best = read.Turns[i].Speaker, length
			}
			read.Turns[i].Speaker = Me
		}
	}
	if loudest == "" {
		return read
	}
	// One voiceprint for the person sitting here — the one from whichever
	// cluster held the floor longest — and the labels that were folded away
	// take their voiceprints with them.
	if print, have := read.Voices[loudest]; have {
		read.Voices[Me] = print
	}
	for label := range read.Voices {
		if label != Me && !stillUsed(read.Turns, label) {
			delete(read.Voices, label)
		}
	}
	return read
}

// Scrap is the most speech a cluster can hold and still be a fragment rather
// than a person. Ten seconds across an hour is not somebody who was in the
// meeting; it is the clusterer noticing that a voice changed.
const Scrap = 10.0

// settle absorbs clusters too small to be anybody into whoever was speaking
// around them.
//
// Only in recordings long enough for the arithmetic to mean something. In a
// two-minute recording ten seconds is a real contribution; in an hour it is
// four rows out of nine hundred, and it puts a SPEAKER_09 in the transcript
// that nobody can match to a human being.
func settle(turns []engine.Turn) []engine.Turn {
	held, total := map[string]float64{}, 0.0
	for _, t := range turns {
		held[t.Speaker] += t.End - t.Start
		total += t.End - t.Start
	}
	if total < 10*60 || len(held) < 3 {
		return turns
	}
	for i, t := range turns {
		if t.Speaker == "" || t.Speaker == Me || held[t.Speaker] > Scrap {
			continue
		}
		// Whoever was speaking just before, or just after at the very start.
		// The audio is contiguous, so the nearest turn is the best guess there
		// is without asking the model something it already answered wrongly.
		if near := neighbour(turns, i, held); near != "" {
			turns[i].Speaker = near
		}
	}
	return turns
}

func neighbour(turns []engine.Turn, at int, held map[string]float64) string {
	for step := 1; step < len(turns); step++ {
		for _, i := range [2]int{at - step, at + step} {
			if i >= 0 && i < len(turns) && held[turns[i].Speaker] > Scrap {
				return turns[i].Speaker
			}
		}
	}
	return ""
}

func stillUsed(turns []engine.Turn, label string) bool {
	for _, t := range turns {
		if t.Speaker == label {
			return true
		}
	}
	return false
}

// Again puts a finished recording back in the queue, from the audio.
//
// Everything derived from it is written over: the transcript, the speakers, the
// passages, the summary. The audio is the only thing that was ever the truth,
// and it is untouched — so this is also how a recording made before some fix
// gets the benefit of it.
func (l *Library) Again(id int64) error {
	r, err := l.db.Get(id)
	if err != nil {
		return err
	}
	if r.Audio == "" {
		return errors.New("the audio has been deleted, so there is nothing left to transcribe")
	}
	if _, err := os.Stat(filepath.Join(l.dir, r.Audio)); err != nil {
		return fmt.Errorf("the recording %s is not in the folder any more", r.Audio)
	}
	if err := l.db.Progress(id, store.Queued, 0); err != nil {
		return err
	}
	l.Wake()
	return nil
}

// Summarise is the button: read this meeting again, now that there is a key or
// now that the speakers have names. Re-runs the summary only — the transcript
// is expensive and has not changed.
func (l *Library) Summarise(ctx context.Context, id int64) error {
	if !l.llm.Ready() {
		return errors.New("summaries need an OpenAI key, which is in Settings")
	}
	rows, err := l.db.Turns(id)
	if err != nil {
		return err
	}
	turns := make([]engine.Turn, len(rows))
	for i, r := range rows {
		turns[i] = engine.Turn{Start: r.Start, End: r.End, Speaker: r.Speaker, Text: r.Text}
	}
	if len(turns) == 0 {
		return errors.New("there is no transcript to summarise")
	}
	return l.summarise(ctx, id, turns)
}

func values(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// summarise is the step that turns a transcript into something worth opening.
// Its failure is recorded but does not fail the recording: the transcript is
// already saved and searchable, and a missing summary is a smaller loss than a
// recording marked failed.
func (l *Library) summarise(ctx context.Context, id int64, turns []engine.Turn) error {
	if !l.llm.Ready() || len(turns) == 0 {
		return l.db.Progress(id, store.Done, 1)
	}
	if err := l.db.Progress(id, store.Summarising, 0.9); err != nil {
		return err
	}

	said := make([]insights.Turn, len(turns))
	for i, t := range turns {
		said[i] = insights.Turn{Start: t.Start, Speaker: t.Speaker, Text: t.Text}
	}
	summary, err := l.llm.Summarise(ctx, said)
	if err != nil {
		slog.Warn("no summary", "id", id, "err", err)
		return l.db.Progress(id, store.Done, 1)
	}
	if err := l.db.SaveSummary(id, translate(summary)); err != nil {
		return err
	}
	slog.Info("summarised", "id", id, "title", summary.Title, "actions", len(summary.ActionItems))
	return l.db.Progress(id, store.Done, 1)
}

// Ask answers a question from the transcripts, and says which passages it used.
func (l *Library) Ask(ctx context.Context, question string) (string, []store.Hit, error) {
	hits, err := l.Find(ctx, question, 20)
	if err != nil {
		return "", nil, err
	}
	if len(hits) == 0 {
		return "", nil, errors.New("nothing in the transcripts covers that")
	}
	answer, err := l.llm.Answer(ctx, question, store.Passages(hits))
	return answer, hits, err
}

// Find is search: what was said about this, however it was phrased.
//
// Both indexes, not one. Keywords find a name, a number or a spelling that a
// vector will happily paraphrase away; vectors find the meeting where somebody
// asked "чи можемо ми взагалі це відкрити назовні" when the question typed was
// "external access". Neither alone is search, and the union is cheap because
// both are already there.
func (l *Library) Find(ctx context.Context, query string, limit int) ([]store.Hit, error) {
	words, err := l.db.Search(query, limit)
	if err != nil {
		return nil, err
	}
	if !l.llm.Ready() {
		return words, nil
	}
	vectors, err := l.llm.Embed(ctx, []string{query})
	if err != nil || len(vectors) == 0 {
		slog.Debug("keyword search only", "err", err)
		return words, nil
	}
	near, err := l.db.Closest(vectors[0], limit)
	if err != nil {
		return words, nil
	}
	return blend(words, near, limit), nil
}

// blend interleaves the two result lists, best of each first, without repeating
// a passage that both found.
//
// Interleaved rather than scored together on purpose: a keyword rank and a
// cosine are not the same quantity, and inventing a formula to add them would
// be a number nobody could check. Taking one from each in turn is honest about
// there being two opinions.
func blend(words, near []store.Hit, limit int) []store.Hit {
	seen := map[string]bool{}
	out := []store.Hit{}
	key := func(h store.Hit) string { return fmt.Sprintf("%d@%.0f", h.Recording, h.Start) }

	for i := 0; len(out) < limit && (i < len(near) || i < len(words)); i++ {
		for _, list := range [][]store.Hit{near, words} {
			if i < len(list) && !seen[key(list[i])] && len(out) < limit {
				seen[key(list[i])] = true
				out = append(out, list[i])
			}
		}
	}
	return out
}

// Delete forgets a recording and removes its audio.
func (l *Library) Delete(id int64) error {
	audio, err := l.db.Delete(id)
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(l.dir, audio))
}

func convert(turns []engine.Turn) []store.Turn {
	out := make([]store.Turn, len(turns))
	for i, t := range turns {
		out[i] = store.Turn{Start: t.Start, End: t.End, Speaker: t.Speaker, Text: t.Text}
	}
	return out
}

func translate(s *insights.Summary) *store.Summary {
	out := &store.Summary{
		Title:         s.Title,
		Overview:      s.Overview,
		Topics:        s.Topics,
		Decisions:     s.Decisions,
		OpenQuestions: s.OpenQuestions,
	}
	for _, c := range s.Chapters {
		out.Chapters = append(out.Chapters, store.Chapter{Start: c.Start, Title: c.Title, Summary: c.Summary})
	}
	for _, a := range s.ActionItems {
		out.ActionItems = append(out.ActionItems, store.Action{Task: a.Task, Owner: a.Owner, Due: a.Due})
	}
	return out
}
