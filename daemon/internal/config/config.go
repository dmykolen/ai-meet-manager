// Package config is the daemon's only knob box: one TOML file, written on
// first run, with defaults that work on the machine it was built for.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/session"
)

// Config is everything the daemon can be told. Durations are TOML strings —
// "10m" rather than 600 — because a number of unspecified units in a file
// somebody edits at midnight is how mistakes happen.
type Config struct {
	// Server is where recordings are sent. Loopback by default and on purpose:
	// an always-on microphone should not be one edit away from posting a
	// meeting to the internet.
	Server string `toml:"server"`

	Spool   string   `toml:"spool"`
	Ring    Duration `toml:"ring"`
	Preroll Duration `toml:"preroll"`

	// StartPaused is for a machine where the daemon should be present but
	// silent until somebody asks for it.
	StartPaused bool `toml:"start_paused"`

	Detect Detect `toml:"detect"`
}

// Detect is when a recording starts and stops. These are the numbers the design
// admits are guesses; leaving one out keeps its default rather than zeroing it.
type Detect struct {
	StartSpeech  Duration `toml:"start_speech"`
	QuietMeeting Duration `toml:"quiet_meeting"`
	QuietNote    Duration `toml:"quiet_note"`
}

// Duration is a TOML string like "10m".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("%q is not a duration like \"10m\": %w", text, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Default is what a fresh install gets.
func Default() Config {
	return Config{
		Server: "http://127.0.0.1:8000",
		Spool:  filepath.Join(stateDir(), "spool"),
		// Ten minutes held, five replayed. The ring is longer than the preroll
		// so that a meeting detected slowly still has its opening in memory.
		Ring:    Duration{10 * time.Minute},
		Preroll: Duration{5 * time.Minute},
		Detect: Detect{
			StartSpeech:  Duration{session.StartSpeech},
			QuietMeeting: Duration{session.QuietMeeting},
			QuietNote:    Duration{session.QuietNote},
		},
	}
}

// Path is where the config lives, honouring MTD_CONFIG for anyone who wants it
// somewhere else.
func Path() string {
	if p := os.Getenv("MTD_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(stateDir(), "mtd.toml")
}

// Load reads the config, writing a commented default the first time so that
// there is something to edit rather than a blank file and a manual.
func Load(path string) (Config, error) {
	cfg := Default()
	switch _, err := os.Stat(path); {
	case os.IsNotExist(err):
		if err := write(path, cfg); err != nil {
			return cfg, err
		}
		return cfg, nil
	case err != nil:
		return cfg, err
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, cfg.check()
}

// Tuning hands the thresholds to the detector in its own terms.
func (c Config) Tuning() session.Tuning {
	return session.Tuning{
		StartSpeech:  c.Detect.StartSpeech.Duration,
		QuietMeeting: c.Detect.QuietMeeting.Duration,
		QuietNote:    c.Detect.QuietNote.Duration,
	}
}

func (c Config) check() error {
	switch {
	case c.Ring.Duration <= 0:
		return fmt.Errorf("ring is %v; it has to hold something", c.Ring)
	case c.Preroll.Duration > c.Ring.Duration:
		// Not an error worth guessing around: the user asked for more history
		// than is being kept, and quietly trimming it would hide the mistake.
		return fmt.Errorf("preroll %v is longer than the %v ring", c.Preroll, c.Ring)
	}
	return nil
}

func write(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	header := `# mtd — the always-on listener. See DAEMON.md.
#
# server       where recordings are sent. Loopback by default: an always-on
#              microphone should not be one edit away from posting to the internet.
# ring         how much audio is held in memory. 3.8 MB per minute.
# preroll      how far into the past a recording reaches when one starts. This is
#              what makes forgetting to press record stop mattering.
# start_paused present but silent until somebody asks.
#
# [detect]     when a recording starts and stops. These are guesses; a week of
#              real use is what should change them.

`
	if _, err := file.WriteString(header); err != nil {
		return err
	}
	return toml.NewEncoder(file).Encode(cfg)
}

// stateDir keeps the spool and the config together, in the place each system
// expects a background program to keep its things.
func stateDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "mtd")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mtd")
}
