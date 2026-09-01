// Package tray is the only thing the daemon shows a person.
//
// It exists for three reasons, and none is decoration. Recording other people is
// not a neutral act, so there is always a visible sign that it is happening and
// a pause that takes one click. Every wait has a name — listening, recording,
// wrapping up, waiting to send — because a background process that goes quiet
// without saying why is indistinguishable from a broken one. And a transcript
// nobody can find was not worth making, so the way to the Library is here.
package tray

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/session"
)

// Model is what the tray needs from the recorder.
type Model interface {
	Status() session.Status
	Paused() bool
	Pause(bool)
	Toggle()
	Recording() bool
}

// Queue is what it needs from the uploader.
type Queue interface {
	Pending() int
	Trouble() string
	Server() string
}

// Run shows the tray and blocks until the user quits or ctx is cancelled.
// It must be called on the main goroutine: macOS will not accept a menu bar
// item from anywhere else.
func Run(ctx context.Context, model Model, queue Queue, spool string, stop context.CancelFunc) {
	systray.Run(func() { onReady(ctx, model, queue, spool, stop) }, stop)
}

type menu struct {
	state, waiting, trouble *systray.MenuItem
	record, pause           *systray.MenuItem
}

func onReady(ctx context.Context, model Model, queue Queue, spool string, stop context.CancelFunc) {
	systray.SetTitle(mark(session.Listening))
	systray.SetTooltip("Meeting Listener")
	slog.Info("menu bar item ready", "spool", spool)

	m := &menu{
		state:   systray.AddMenuItem("Starting…", ""),
		waiting: systray.AddMenuItem("", "Recordings waiting to be sent"),
		trouble: systray.AddMenuItem("", "Something is wrong"),
	}
	// Left enabled: when the phase is NeedsPermission this line is the fix.
	m.waiting.Disable()
	m.trouble.Disable()
	systray.AddSeparator()

	// First, because it is the reason any of this exists.
	library := systray.AddMenuItem("Open transcripts", "Everything recorded so far")
	systray.AddSeparator()

	m.record = systray.AddMenuItem("Record now", "Start a recording this moment, and keep it running until you stop it")
	m.pause = systray.AddMenuItem("Pause listening", "Stop capturing until resumed")
	systray.AddSeparator()

	folder := systray.AddMenuItem("Show recordings folder", spool)
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Stop the listener")

	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				systray.Quit()
				return
			case <-tick.C:
			case <-m.state.ClickedCh:
				if model.Status().Phase == session.NeedsPermission {
					open("x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone")
				}
			case <-library.ClickedCh:
				open(queue.Server())
			case <-m.record.ClickedCh:
				model.Toggle()
			case <-m.pause.ClickedCh:
				model.Pause(!model.Paused())
			case <-folder.ClickedCh:
				open(spool)
			case <-quit.ClickedCh:
				stop()
				systray.Quit()
				return
			}
			refresh(model, queue, m)
		}
	}()
}

func refresh(model Model, queue Queue, m *menu) {
	status := model.Status()
	systray.SetTitle(mark(status.Phase))
	m.state.SetTitle(describe(status))

	show(m.waiting, plural(queue.Pending(), "recording", "still to send"))
	show(m.trouble, queue.Trouble())

	if model.Recording() {
		m.record.SetTitle("Stop recording")
	} else {
		m.record.SetTitle("Record now")
	}
	if model.Paused() {
		m.pause.SetTitle("Resume listening")
	} else {
		m.pause.SetTitle("Pause listening")
	}
}

// show hides an item rather than leaving it blank, so the menu is only as long
// as there is something to say.
func show(item *systray.MenuItem, text string) {
	if text == "" {
		item.Hide()
		return
	}
	item.SetTitle(text)
	item.Show()
}

func plural(n int, noun, tail string) string {
	switch n {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("1 %s %s", noun, tail)
	default:
		return fmt.Sprintf("%d %ss %s", n, noun, tail)
	}
}

// describe names the wait. "Recording" alone would not say whether anything is
// still being heard, which is the question somebody looks at this to answer.
func describe(s session.Status) string {
	switch s.Phase {
	case session.Recording:
		return fmt.Sprintf("Recording a %s — %s", s.Kind, short(s.Elapsed))
	case session.WrappingUp:
		return fmt.Sprintf("Wrapping up — quiet for %s", short(s.Quiet))
	case session.Paused:
		return "Paused — nothing is being captured"
	case session.Opening:
		return "Starting up — opening the microphone"
	case session.NeedsPermission:
		return "Needs permission — click here to fix it"
	default:
		return "Listening"
	}
}

// mark is the menu bar itself, which is the part somebody sees without looking.
func mark(phase session.Phase) string {
	switch phase {
	case session.Recording:
		return "● REC"
	case session.WrappingUp:
		return "◐ REC"
	case session.Paused:
		return "❙❙"
	case session.NeedsPermission:
		return "○ !"
	default:
		return "○"
	}
}

func short(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func open(target string) {
	command := map[string]string{"darwin": "open", "windows": "explorer"}[runtime.GOOS]
	if command == "" {
		command = "xdg-open"
	}
	_ = exec.Command(command, target).Start()
}
