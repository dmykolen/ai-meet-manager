package home

import (
	"os"
	"path/filepath"
	"testing"
)

// A settings file written by an older version of the app must never be able to
// stop the app starting. This one could: `summarise` went from a switch to a
// choice, the decode failed, Load returned an error, and the window never
// opened — with the reason written to a log file that had already been closed.
func TestSettingsFromAnOlderVersionStillOpen(t *testing.T) {
	dir := t.TempDir()
	old := `
language = "uk"
openai_key = "sk-test"
summarise = true

[listen]
  enabled = true
  start_speech = "20s"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("an old settings file stopped the app: %v", err)
	}
	if cfg.Summarise != "always" {
		t.Fatalf("summarise = %q; the old true meant every recording", cfg.Summarise)
	}
	// And nothing else in the file was lost on the way.
	if cfg.OpenAIKey != "sk-test" || cfg.Language != "uk" {
		t.Fatalf("the rest of the settings did not survive: %+v", cfg)
	}
	if !cfg.Listen.Enabled || cfg.Listen.StartSpeech.Seconds() != 20 {
		t.Fatalf("the listening settings did not survive: %+v", cfg.Listen)
	}
}

func TestAnUnreadableSettingCostsTheSettingAndNotTheApp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte("language = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("a broken settings file stopped the app: %v", err)
	}
	if cfg.Language != Defaults().Language {
		t.Fatalf("language = %q, want the default", cfg.Language)
	}
}

func TestAutoIsTheOnlyWayToAskForDetection(t *testing.T) {
	// Empty means "nobody has chosen", not "detect it" — which is what stops a
	// Ukrainian meeting with a few borrowed words coming back in Russian.
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != "uk" || cfg.Spoken() != "uk" {
		t.Fatalf("a fresh install speaks %q", cfg.Language)
	}
	cfg.Language = Auto
	if cfg.Spoken() != "" {
		t.Fatalf("auto gave the model %q instead of letting it detect", cfg.Spoken())
	}
}

func TestSettingsSurviveBeingWrittenAndReadBack(t *testing.T) {
	dir := t.TempDir()
	cfg := Defaults()
	cfg.Summarise = "never"
	cfg.Listen.KeepNotes = true
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.Summarise != "never" || !back.Listen.KeepNotes {
		t.Fatalf("read back as %q / keep_notes %v", back.Summarise, back.Listen.KeepNotes)
	}
}
