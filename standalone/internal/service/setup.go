package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/models"
)

// Stage is what the app is doing before it can transcribe anything.
type Stage string

const (
	Downloading Stage = "downloading" // fetching models on first run
	Loading     Stage = "loading"     // opening them, which takes seconds
	Ready       Stage = "ready"
	Broken      Stage = "broken"
)

// State is the first-run screen, and afterwards the thing that says whether the
// app can work at all.
type State struct {
	Stage    Stage   `json:"stage"`
	What     string  `json:"what"`     // which model, in words
	Fraction float64 `json:"fraction"` // 0..1 across the whole download
	Done     int64   `json:"done"`
	Total    int64   `json:"total"`
	Problem  string  `json:"problem,omitempty"`
}

// Setup drives the first run and reports it.
//
// It is a service of its own because the interface needs to ask "can I show the
// library yet?" before anything else exists, and because a 582 MB download on a
// hotel connection is a screen, not a spinner.
type Setup struct {
	dir   string
	extra models.Set

	mu    sync.Mutex
	state State
	ready chan struct{}
	once  sync.Once
}

func NewSetup(modelsDir string) *Setup {
	return &Setup{
		dir:   modelsDir,
		state: State{Stage: Downloading},
		ready: make(chan struct{}),
	}
}

// Want adds a model that a setting asked for — Parakeet, today. Chosen rather
// than required, so it is not in everybody's first run.
func (s *Setup) Want(extra models.Set) { s.extra = extra }

// Status is what the interface is given: one method, deliberately.
//
// Setup itself is not bound. Its other methods take a context and return a Go
// channel, neither of which has a shape in TypeScript — and the binding
// generator does not decline them, it crashes.
type Status struct{ setup *Setup }

// Bound returns the narrow view of this Setup for the frontend.
func (s *Setup) Bound() *Status { return &Status{setup: s} }

// State is polled by the first-run screen.
func (s *Status) State() State { return s.setup.snapshot() }

func (s *Setup) snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Wait blocks until the models are on disk, which is what the rest of the app
// waits on before loading anything.
func (s *Setup) Wait() <-chan struct{} { return s.ready }

// Fetch downloads whatever is missing. Safe to call when nothing is.
func (s *Setup) Fetch(ctx context.Context) {
	missing := append(models.Required(), s.extra...).Missing(s.dir)
	if len(missing) == 0 {
		s.set(State{Stage: Loading, What: "Opening the models"})
		s.finish()
		return
	}

	total := missing.Size()
	slog.Info("first run: fetching models", "count", len(missing), "mb", total/1048576)
	s.set(State{Stage: Downloading, What: missing[0].Name, Total: total})

	report := make(chan models.Progress, 8)
	go models.Fetch(ctx, s.dir, missing, report)

	var finished int64
	for p := range report {
		if p.Err != nil {
			s.set(State{Stage: Broken, What: p.Model, Problem: p.Err.Error()})
			slog.Error("model download failed", "model", p.Model, "err", p.Err)
			return
		}
		if p.Finished {
			finished += p.Size
			continue
		}
		done := finished + p.Done
		s.set(State{
			Stage:    Downloading,
			What:     p.Model,
			Done:     done,
			Total:    total,
			Fraction: min(float64(done)/float64(total), 1),
		})
	}

	s.set(State{Stage: Loading, What: "Opening the models", Fraction: 1, Done: total, Total: total})
	s.finish()
}

// Loaded is called once the engine is up, which is the last thing between a
// fresh install and a working app.
func (s *Setup) Loaded(err error) {
	if err != nil {
		s.set(State{Stage: Broken, What: "Loading the models", Problem: err.Error()})
		return
	}
	s.set(State{Stage: Ready, Fraction: 1})
}

func (s *Setup) set(state State) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}

func (s *Setup) finish() { s.once.Do(func() { close(s.ready) }) }

// Human renders a byte count the way a download dialog should.
func Human(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGT"[exp])
}
