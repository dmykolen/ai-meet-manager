package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAMissingConfigIsWrittenNotDemanded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mtd.toml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != Default().Server {
		t.Fatalf("server is %q on a fresh install", cfg.Server)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("nothing was written to edit: %v", err)
	}
	if !strings.Contains(string(written), "preroll") {
		t.Fatalf("the file has no preroll to edit:\n%s", written)
	}
	// Written back out, it has to be readable again.
	if _, err := Load(path); err != nil {
		t.Fatalf("the config it wrote cannot be read: %v", err)
	}
}

func TestDurationsAreWrittenAsPeopleReadThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mtd.toml")
	Load(path)
	written, _ := os.ReadFile(path)
	if !strings.Contains(string(written), `ring = "10m0s"`) {
		t.Fatalf("ring is not a readable duration:\n%s", written)
	}
}

func TestSettingsAreRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mtd.toml")
	os.WriteFile(path, []byte(`
server = "http://127.0.0.1:8010"
ring = "2m"
preroll = "30s"
start_paused = true
`), 0o644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "http://127.0.0.1:8010" {
		t.Fatalf("server=%q", cfg.Server)
	}
	if cfg.Ring.Duration != 2*time.Minute || cfg.Preroll.Duration != 30*time.Second {
		t.Fatalf("ring=%v preroll=%v", cfg.Ring, cfg.Preroll)
	}
	if !cfg.StartPaused {
		t.Fatal("start_paused was ignored")
	}
	if cfg.Spool == "" {
		t.Fatal("a setting that was not named lost its default")
	}
}

func TestAPrerollLongerThanTheRingIsRefused(t *testing.T) {
	// Quietly trimming it would hide the mistake: the user would be told they
	// have ten minutes of history and get two.
	path := filepath.Join(t.TempDir(), "mtd.toml")
	os.WriteFile(path, []byte("ring = \"2m\"\npreroll = \"10m\"\n"), 0o644)

	if _, err := Load(path); err == nil {
		t.Fatal("a preroll longer than the ring was accepted")
	}
}

func TestANonsenseDurationSaysWhatItWanted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mtd.toml")
	os.WriteFile(path, []byte(`ring = "ten minutes"`), 0o644)

	_, err := Load(path)
	if err == nil {
		t.Fatal(`"ten minutes" was accepted`)
	}
	if !strings.Contains(err.Error(), `"10m"`) {
		t.Fatalf("the error does not show the right shape: %v", err)
	}
}

func TestTheDefaultServerIsLoopback(t *testing.T) {
	// An always-on microphone should not be one edit away from posting a
	// meeting to the internet.
	if got := Default().Server; !strings.Contains(got, "127.0.0.1") {
		t.Fatalf("the default server is %q", got)
	}
}
