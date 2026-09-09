// Package home is the one folder the app owns.
//
// Everything it ever writes lives under ~/MeetingTranscriber: the database, the
// recordings, the models it downloads on first run, the config and the log. It
// is deliberately in the home folder rather than tucked away in Library, so
// that somebody who wants their recordings can find them, and somebody who
// wants the app gone can drag one folder to the Bin and be done.
package home

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Folder is the name people will see in Finder. Chosen to sort near the top and
// to be obvious at a glance.
const Folder = "MeetingTranscriber"

// Dir is the working folder, created if it is not there yet.
func Dir() (string, error) {
	if override := os.Getenv("MT_HOME"); override != "" {
		return override, os.MkdirAll(override, 0o755)
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, Folder), os.MkdirAll(filepath.Join(base, Folder), 0o755)
}

// The four places things go. Each is created on demand rather than all at
// startup, so a folder that exists is a folder with something in it.
func Recordings(dir string) string { return sub(dir, "recordings") }
func Models(dir string) string     { return sub(dir, "models") }
func Logs(dir string) string       { return sub(dir, "logs") }

// Cache holds derived files that can always be made again — today, the folded
// mono copy the player is served. Safe to delete at any time.
func Cache(dir string) string    { return sub(dir, "cache") }
func Database(dir string) string { return filepath.Join(dir, "meetings.db") }

func sub(dir, name string) string {
	path := filepath.Join(dir, name)
	_ = os.MkdirAll(path, 0o755)
	return path
}

// Config is everything the app can be told. There is not much, on purpose: a
// desktop app that needs its settings edited has already failed.
type Config struct {
	// Language of the meetings, or Auto to work it out per meeting. Ukrainian
	// by default: left to detect, Whisper hears a Ukrainian meeting with a few
	// borrowed words and writes it down as Russian, which is both wrong and
	// unwelcome. Naming the language costs nothing and settles it.
	Language string `toml:"language"`

	// OpenAI is only used for summaries, questions and semantic search.
	// Transcription and speaker detection never leave the machine.
	OpenAIKey   string `toml:"openai_key"`
	OpenAIModel string `toml:"openai_model"`

	// Summarise says which recordings are worth a model call: "always",
	// "meetings" or "never". Meetings by default — most of what an always-on
	// recorder catches is half a phone call or a thought said out loud, and
	// summarising those is money spent on nothing. The button is always there.
	Summarise Choice `toml:"summarise"`

	// Density of the transcript: "compact" or "comfortable". A preference, but
	// it belongs with the rest rather than in browser storage, so that it
	// survives reinstalling and follows the folder.
	Density string `toml:"density"`

	// Transcriber is "whisper" or "parakeet". Whisper by default, and the
	// comment in engine/parakeet.go has the measurements that put it there.
	Transcriber string `toml:"transcriber"`

	// Me is what the person holding the laptop is called, set by "This is me"
	// in the settings. Everything the microphone hears is them, so this is the
	// one identity the app never has to guess.
	Me string `toml:"me"`

	Listen Listen `toml:"listen"`
	Keep   Keep   `toml:"keep"`
}

// Listen is the always-on part: when a recording starts and stops.
type Listen struct {
	Enabled bool `toml:"enabled"`

	// System decides whether the machine's own audio is captured alongside the
	// microphone. On by default, and it is what the app knows that a generic
	// recorder does not: speech on the system channel is somebody talking to
	// you, which is the whole meeting detector, and it gives the far side clean
	// rather than through the room.
	//
	// Off, the app is Voice Memos with a transcript. It cannot tell a meeting
	// from thinking aloud any more, so everything it starts by itself is a
	// note and KeepNotes decides whether it survives.
	System bool `toml:"system"`

	// KeepNotes decides what happens to a recording nobody else was in and
	// nobody asked for. Off by default: talking to yourself, or to somebody on
	// the phone, is hours a week of audio that costs a transcription and a
	// summary and is worth neither. Press Record and the note is always kept,
	// whatever this says.
	KeepNotes bool `toml:"keep_notes"`

	StartSpeech Duration `toml:"start_speech"`
	QuietEnds   Duration `toml:"quiet_ends"`
	Preroll     Duration `toml:"preroll"`
	Ring        Duration `toml:"ring"`
}

// Keep is what is thrown away and when. Transcripts are kept for ever; audio is
// large and usually stops being interesting.
type Keep struct {
	AudioDays int `toml:"audio_days"`
}

// Choice is a setting that used to be a switch. Changing a key's type made the
// app refuse to start on every machine holding the old one: decode failed, Load
// errored, the window never opened. The two old values are read as what they
// meant.
type Choice string

func (c *Choice) UnmarshalTOML(v any) error {
	switch value := v.(type) {
	case string:
		*c = Choice(value)
	case bool:
		*c = "never"
		if value {
			*c = "always"
		}
	default:
		return fmt.Errorf("%v is not a setting this understands", v)
	}
	return nil
}

func (c Choice) MarshalTOML() ([]byte, error) { return []byte(`"` + string(c) + `"`), nil }

// Duration is written the way people say it — "20s", "3m" — rather than as a
// number of unspecified units.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("%q is not a duration like \"3m\": %w", text, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Auto is the language setting that means "work it out from the audio".
const Auto = "auto"

// Spoken is the language to hand a model: empty when it should detect.
func (c Config) Spoken() string {
	if strings.EqualFold(c.Language, Auto) {
		return ""
	}
	return c.Language
}

// Defaults are what a fresh install runs with. Every one of them is a guess
// that a week of real use should correct.
func Defaults() Config {
	return Config{
		Language:    "uk",
		Transcriber: "whisper",
		OpenAIModel: "gpt-5.4-mini",
		Summarise:   "meetings",
		Density:     "compact",
		Listen: Listen{
			Enabled:     true,
			System:      true,
			KeepNotes:   false,
			StartSpeech: Duration{20 * time.Second},
			QuietEnds:   Duration{3 * time.Minute},
			Preroll:     Duration{5 * time.Minute},
			Ring:        Duration{10 * time.Minute},
		},
		Keep: Keep{AudioDays: 30},
	}
}

// Load reads the config, writing a commented default the first time so that
// there is something to edit rather than a blank file and a manual.
func Load(dir string) (Config, error) {
	path := filepath.Join(dir, "config.toml")
	cfg := Defaults()

	switch _, err := os.Stat(path); {
	case os.IsNotExist(err):
		return cfg, write(path, cfg)
	case err != nil:
		return cfg, err
	}
	// A settings file this cannot read costs the settings, never the app. It
	// started as defaults, so whatever did decode is kept and the rest is what
	// a fresh install would have used.
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		slog.Warn("some settings could not be read and are back at their defaults",
			"file", path, "err", err)
	}
	// An empty key in the file means "not set", not "the empty key" — otherwise
	// a commented-out line and a blank line behave differently for no reason.
	if cfg.OpenAIKey == "" {
		cfg.OpenAIKey = os.Getenv("OPENAI_API_KEY")
	}
	// Same for the language: empty means nobody has chosen, not "detect it".
	// Detection is asked for by name, because leaving Whisper to guess is how a
	// Ukrainian meeting with a few borrowed words ends up written in Russian.
	if cfg.Language == "" {
		cfg.Language = Defaults().Language
	}
	if cfg.Transcriber == "" {
		cfg.Transcriber = Defaults().Transcriber
	}
	if cfg.Summarise == "" {
		cfg.Summarise = Defaults().Summarise
	}
	return cfg, nil
}

// Save writes the config back, which is how the settings screen persists.
func Save(dir string, cfg Config) error {
	return write(filepath.Join(dir, "config.toml"), cfg)
}

func write(path string, cfg Config) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	const header = `# Meeting Transcriber
#
# Everything this app owns lives in this folder. Delete it and the app is gone.
#
# openai_key    only for summaries, questions and semantic search. Transcription
#               and speaker detection run on this machine and never send audio
#               anywhere. Leave it empty and those features simply stay off.
# language      "uk", "en", … or "auto" to work it out per meeting. Naming it is
#               more accurate than detecting it, and it is what stops a
#               Ukrainian meeting being written down as Russian.
# summarise     "always", "meetings" or "never". A note nobody else was in is
#               usually a phone call or a thought said out loud.
# me            your name. Everything the microphone hears is you, so this is
#               applied directly rather than recognised.
# transcriber   "whisper" or "parakeet". Parakeet is faster and, on clean read
#               speech, more accurate on Ukrainian — but it picks the language
#               itself and cannot be told, and on a meeting recorded through a
#               room it found half the words Whisper did.
# [listen]      when a recording starts and stops on its own.
# [keep]        audio_days = 0 keeps recordings for ever.

`
	if _, err := file.WriteString(header); err != nil {
		return err
	}
	return toml.NewEncoder(file).Encode(cfg)
}
