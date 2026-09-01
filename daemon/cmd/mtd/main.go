// Command mtd listens in the background so that nobody has to remember to press
// record. See DAEMON.md for the design.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/config"
	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/session"
	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/source"
	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/tray"
	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/upload"
)

func main() {
	var (
		probe    = flag.Duration("probe", 0, "capture for this long, report the level of both channels, and exit")
		record   = flag.String("record", "", "with -probe, also write the capture to this stereo WAV")
		headless = flag.Bool("headless", false, "run without a menu bar item")
		verbose  = flag.Bool("verbose", false, "log every decision, not just what happened")
	)
	flag.Parse()

	// The ring buffer is 38 MB of permanently live data, and the mixer hands out
	// a fresh 2 KB frame thirty-one times a second. At the default the collector
	// lets the heap grow to twice the live set, which on a program that is idle
	// anyway is paying memory for CPU nobody wants back. Measured: 201 MB RSS
	// before, 143 MB after, with no change in CPU.
	debug.SetGCPercent(40)

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, stop, *probe, *record, *headless); err != nil {
		slog.Error("mtd stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, stop context.CancelFunc, probe time.Duration, record string, headless bool) error {
	if probe > 0 {
		return runProbe(ctx, probe, record)
	}

	cfg, err := config.Load(config.Path())
	if err != nil {
		return err
	}
	slog.Info("mtd starting",
		"config", config.Path(),
		"server", cfg.Server,
		"spool", cfg.Spool,
		"ring", cfg.Ring, "preroll", cfg.Preroll)

	lock, err := session.Take(cfg.Spool)
	if err != nil {
		return err
	}
	defer lock.Release()

	uploader, err := upload.New(cfg.Spool, cfg.Server)
	if err != nil {
		return err
	}
	loading := time.Now()
	slog.Info("loading the voice detector")
	recorder, err := session.NewRecorder(uploader, cfg.Ring.Duration, cfg.Preroll.Duration, cfg.Tuning())
	if err != nil {
		return err
	}
	slog.Info("voice detector ready", "took", time.Since(loading).Round(time.Millisecond))
	if cfg.StartPaused {
		recorder.Pause(true)
	}
	session.Recover(cfg.Spool)
	if pending := uploader.Pending(); pending > 0 {
		slog.Info("recordings from a previous run are waiting", "count", pending)
	}
	go uploader.Run(ctx, recorder.Busy)

	// The menu bar item comes up before the microphone does, and the capture
	// loop is what waits. Opening the devices first meant that a missing
	// permission showed as no icon at all — the one state in which a person most
	// needs to be told something, and the one in which they were told nothing.
	go listen(ctx, recorder)

	if headless {
		<-ctx.Done()
		return nil
	}
	tray.Run(ctx, recorder, uploader, cfg.Spool, stop)
	return nil
}

// listen opens the devices and runs the capture loop, retrying for as long as
// the daemon is up.
//
// It retries rather than exiting because the usual reason it cannot start is a
// permission somebody is about to grant. Exiting would take the menu bar item —
// and with it the explanation — away at the exact moment it is needed.
func listen(ctx context.Context, recorder *session.Recorder) {
	for ctx.Err() == nil {
		recorder.Report(session.Opening)
		slog.Info("opening the audio devices")

		// Open blocks while macOS waits for a person to answer the permission
		// dialog. That is not a fault, so it is not timed out — it is named, in
		// the menu bar, where somebody can act on it.
		opened := make(chan struct{})
		go func() {
			select {
			case <-opened:
			case <-ctx.Done():
			case <-time.After(source.Waiting):
				recorder.Report(session.NeedsPermission)
			}
		}()

		opening := time.Now()
		stream, err := source.Open(ctx)
		close(opened)
		if err != nil {
			slog.Error("cannot listen", "err", err)
			recorder.Report(session.NeedsPermission)
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
			continue
		}

		slog.Info("audio devices open",
			"took", time.Since(opening).Round(time.Millisecond),
			"system_audio", stream.SystemAudio)
		if !stream.SystemAudio {
			slog.Warn("no system audio: other people will not be recorded, " +
				"and every conversation will be filed as a note")
		}

		recorder.Report(session.Listening)
		err = recorder.Run(ctx, stream.Frames())
		stream.Close()
		if err != nil {
			slog.Error("capture stopped", "err", err)
		}
	}
}

// runProbe answers the only question that matters before anything else is
// built: does each channel carry real audio?
//
// It exists because a refused macOS permission is invisible — the tap is
// created, the device starts, the stream begins, and every sample is zero. A
// level meter is the only thing that tells the difference.
func runProbe(ctx context.Context, d time.Duration, path string) error {
	// The first launch after `make install` waits about a minute here while
	// macOS vets a freshly signed bundle before letting it near the microphone.
	// Measured: 63s the first time, 0.3s every time after. Saying so beats a
	// minute of silence that looks exactly like a hang.
	opening := time.Now()
	slog.Info("opening the audio devices")
	stream, err := source.Open(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	slog.Info("audio devices open", "took", time.Since(opening).Round(time.Millisecond))

	var rec *session.Writer
	if path != "" {
		if rec, err = session.Create(path); err != nil {
			return err
		}
		defer rec.Close() // safety net; the real close is below, where its error matters
	}

	if !stream.SystemAudio {
		slog.Warn("no system audio device; the right channel will be silent")
	}
	fmt.Printf("listening for %s — say something, and play something\n\n", d)

	const framesPerTick = source.SampleRate / source.FrameSize / 2 // twice a second
	var (
		deadline           = time.After(d)
		mic, sys           channel // reset every tick, for the bars
		micTotal, sysTotal channel // kept, for the verdict
		frames             int
	)

	for done := false; !done; {
		select {
		case <-ctx.Done():
			done = true
		case <-deadline:
			done = true
		case frame, ok := <-stream.Frames():
			if !ok {
				done = true
				break
			}
			if rec != nil {
				if err := rec.Write(frame); err != nil {
					return err
				}
			}
			for i := 0; i < len(frame); i += 2 {
				mic.add(frame[i])
				micTotal.add(frame[i])
				sys.add(frame[i+1])
				sysTotal.add(frame[i+1])
			}
			if frames++; frames%framesPerTick == 0 {
				fmt.Printf("  %s   %s\n", bar("mic", mic), bar("sys", sys))
				mic, sys = channel{}, channel{}
			}
		}
	}

	fmt.Println()
	if rec != nil {
		took := rec.Duration()
		if err := rec.Close(); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%s)\n\n", path, took)
	}
	report("microphone", micTotal)
	report("system    ", sysTotal)
	if micTotal.peak == 0 || (stream.SystemAudio && sysTotal.peak == 0) {
		return fmt.Errorf("a channel captured pure silence — grant the permission in " +
			"System Settings > Privacy & Security, then run this again")
	}
	return nil
}
