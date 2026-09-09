// Command mt is Meeting Transcriber: one binary that records meetings, writes
// them down, works out who said what, and summarises them.
//
// Nothing here talks to a server. The models run on this machine; the only
// thing that leaves it is the text of a meeting, and only when there is a key
// to send it with.
package main

import (
	"context"
	"embed"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/engine"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/home"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/insights"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/library"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/listen"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/media"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/models"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/service"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/store"
)

//go:embed all:frontend/dist
var assets embed.FS

// sound serves the recordings folder at /audio/<file>, so a transcript can be
// listened to while it is read.
//
// http.ServeFile rather than reading the file ourselves, because it answers
// range requests — which is what lets an audio element seek an hour-long file
// instead of downloading 230 MB before it will play a note.
func sound(dir, cache string) application.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// net/http has already decoded the path, so unescaping again here
			// would corrupt any name containing a per-cent sign.
			name, is := strings.CutPrefix(r.URL.Path, "/audio/")
			if !is {
				next.ServeHTTP(w, r)
				return
			}
			// The name comes from the database, but it reaches us through the
			// webview, so it is checked rather than trusted. Base of itself
			// means no directory anywhere in it, "..", "/" and all.
			if name == "" || name != filepath.Base(name) {
				http.NotFound(w, r)
				return
			}
			// Folded to mono first. The raw file has the far side twice over —
			// once from the tap and once through the room — and playing that
			// back is an echo on every sentence somebody else said.
			listen, err := media.Listenable(filepath.Join(dir, name), cache)
			if err != nil {
				slog.Warn("playing the raw recording", "file", name, "err", err)
			}
			http.ServeFile(w, r, listen)
		})
	}
}

func main() {
	// The log is opened here, not in run, because run's defers have already
	// closed everything by the time it returns — which meant that the one
	// message worth having, the reason the app would not start, was written to
	// a closed file and lost. Found by an app that exited 1 in silence.
	dir, err := home.Dir()
	if err == nil {
		if logs, err := os.OpenFile(filepath.Join(home.Logs(dir), "mt.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			defer logs.Close()
			slog.SetDefault(slog.New(slog.NewTextHandler(
				io.MultiWriter(logs, os.Stderr), &slog.HandlerOptions{Level: slog.LevelInfo})))
		}
	}
	if err := run(); err != nil {
		slog.Error("Meeting Transcriber stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dir, err := home.Dir()
	if err != nil {
		return err
	}
	slog.Info("starting", "folder", dir)
	// Where the decoders the app downloaded live, so a machine with an ancient
	// ffmpeg on its PATH does not get to decide what this app can open.
	media.Tools = models.Tools(home.Models(dir))

	cfg, err := home.Load(dir)
	if err != nil {
		return err
	}
	if cfg.OpenAIKey == "" {
		slog.Warn("no OpenAI key: summaries, the to-do list and Ask are off. " +
			"Add one in Settings — transcription and speakers run here either way")
	}
	db, err := store.Open(home.Database(dir))
	if err != nil {
		return err
	}
	defer db.Close()

	// Voiceprints saved before they carried a source are matched back to the
	// meetings they came from, so a sample can be played rather than taken on
	// trust. Does nothing on every start after the first.
	if err := db.Trace(); err != nil {
		slog.Warn("could not trace where the saved voices came from", "err", err)
	}

	// The models are fetched and loaded behind the window rather than in front
	// of it: the app appears at once and says what it is doing, instead of
	// spending the first two minutes of its life as a bouncing icon.
	setup := service.NewSetup(home.Models(dir))
	// Parakeet is 670 MB and is not the default; it is downloaded only once
	// somebody has chosen it in the settings.
	setup.Want(models.Optional(cfg.Transcriber))
	lib := library.New(db, nil, insights.New(cfg.OpenAIKey, cfg.OpenAIModel, cfg.Language), home.Recordings(dir))
	lib.Policy(store.When(cfg.Summarise))
	lib.Owner(cfg.Me)
	meetings := service.New(db, lib, dir, cfg)

	// Anything left half-written when the app last died holds no readable
	// audio, and should be reported rather than left to puzzle somebody.
	listen.Recover(home.Recordings(dir))

	go func() {
		setup.Fetch(ctx)
		select {
		case <-setup.Wait():
		case <-ctx.Done():
			return
		}
		e, err := engine.Open(home.Models(dir), engine.Options{
			Language:    cfg.Spoken(),
			Threads:     runtime.NumCPU(),
			Transcriber: cfg.Transcriber,
		})
		setup.Loaded(err)
		if err != nil {
			slog.Error("the models would not load", "err", err)
			return
		}
		defer e.Close()
		slog.Info("models ready")
		lib.Use(e)

		// The listener needs one of the same models, so it starts here rather
		// than at launch. It runs alongside the queue: transcribing a finished
		// meeting must never stop the app hearing the next one.
		ears := listen.New(home.Recordings(dir),
			models.Path(home.Models(dir), models.Speech), cfg.Listen,
			func(path string, kind store.Kind, started time.Time) {
				if _, err := lib.Add(kind, path, started, ""); err != nil {
					slog.Error("could not file a recording", "path", path, "err", err)
				}
			},
			func(seconds float64, why string) {
				if err := db.Skipped(seconds, why); err != nil {
					slog.Warn("could not record what was discarded", "err", err)
				}
			},
			// The live transcript. One utterance at a time through the same
			// Whisper the queue uses, which is why the queue steps aside below.
			func(samples []float32) (string, error) {
				turns, err := e.Transcribe(samples)
				if err != nil {
					return "", err
				}
				said := make([]string, len(turns))
				for i, t := range turns {
					said[i] = t.Text
				}
				return strings.TrimSpace(strings.Join(said, " ")), nil
			},
			// How the listener tells one voice from another when it has only
			// the microphone to go on. The same embedder the speaker models
			// use, so "a different person" means the same thing everywhere.
			e.Print)
		meetings.Listener(ears)
		go ears.Run(ctx)

		// Transcribing yesterday's meeting takes every core the machine has. Do
		// it while today's is being recorded and the fans come on during the
		// call, the live transcript falls minutes behind, and both jobs are
		// worse. The queue waits; nothing is lost but a few minutes.
		lib.Wait(ears.Recording)

		// Old audio goes on a schedule the settings own. Transcripts stay.
		go lib.Tidy(ctx.Done(), func() int { return meetings.Settings().KeepAudioDays })

		lib.Run(ctx)
	}()

	app := application.New(application.Options{
		Name:        "Meeting Transcriber",
		Description: "Records meetings, writes them down, and tells you what was decided.",
		Services: []application.Service{
			application.NewService(meetings),
			application.NewService(setup.Bound()),
		},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(assets),
			// One route of our own, for playing a recording back. Everything
			// else falls through to the embedded interface.
			Middleware: sound(home.Recordings(dir), home.Cache(dir)),
		},
		Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Meeting Transcriber",
		Width:  1180,
		Height: 800,
		// Small enough to sit beside a call, which is where it will live.
		MinWidth:         900,
		MinHeight:        600,
		BackgroundColour: application.NewRGB(11, 11, 14),
		Mac: application.MacWindow{
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
			InvisibleTitleBarHeight: 42,
		},
	})

	go func() {
		<-ctx.Done()
		app.Quit()
	}()
	return app.Run()
}
