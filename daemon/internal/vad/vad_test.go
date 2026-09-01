package vad

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// The model and the runtime live in the build tree, which a test binary running
// from a temporary directory cannot find on its own.
func TestMain(m *testing.M) {
	root, err := filepath.Abs("../../runtime")
	if err != nil {
		panic(err)
	}
	os.Setenv("MTD_ONNXRUNTIME", filepath.Join(root, libraryName))
	os.Setenv("MTD_SILERO_VAD", filepath.Join(root, "silero_vad.onnx"))
	os.Exit(m.Run())
}

func detector(t *testing.T) *Detector {
	t.Helper()
	d, err := New(16000)
	if err != nil {
		t.Fatalf("load Silero: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// share reports the fraction of frames the detector called speech, which is the
// only honest way to judge a per-frame model: no single frame is decisive.
func share(t *testing.T, d *Detector, samples []int16) float64 {
	t.Helper()
	speech, total := 0, 0
	for i := 0; i+Window <= len(samples); i += Window {
		yes, err := d.IsSpeech(samples[i : i+Window])
		if err != nil {
			t.Fatal(err)
		}
		if yes {
			speech++
		}
		total++
	}
	if total == 0 {
		t.Fatal("no complete frames")
	}
	return float64(speech) / float64(total)
}

func TestRealSpeechIsDetected(t *testing.T) {
	// Recorded by the daemon itself through the microphone, so this is the
	// signal path the detector will actually see, not a clean studio sample.
	got := share(t, detector(t), readWAV(t, "testdata/speech.wav"))
	if got < 0.6 {
		t.Fatalf("only %.0f%% of frames were called speech; the fixture is people talking", got*100)
	}
	t.Logf("speech recording: %.0f%% of frames called speech", got*100)
}

func TestSilenceIsNotSpeech(t *testing.T) {
	if got := share(t, detector(t), make([]int16, 32*Window)); got > 0.05 {
		t.Fatalf("%.0f%% of silent frames were called speech", got*100)
	}
}

func TestNoiseIsNotSpeech(t *testing.T) {
	// The reason Silero is here rather than WebRTC's detector: a room with a fan
	// in it must not look like a conversation.
	rng := rand.New(rand.NewSource(1))
	noise := make([]int16, 64*Window)
	for i := range noise {
		noise[i] = int16(rng.NormFloat64() * 1500)
	}
	got := share(t, detector(t), noise)
	if got > 0.25 {
		t.Fatalf("%.0f%% of white-noise frames were called speech", got*100)
	}
	t.Logf("white noise: %.0f%% of frames called speech", got*100)
}

func TestASteadyToneIsNotSpeech(t *testing.T) {
	// A hum, a doorbell, a held note — loud, but nobody is talking.
	tone := make([]int16, 64*Window)
	for i := range tone {
		tone[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/16000))
	}
	if got := share(t, detector(t), tone); got > 0.25 {
		t.Fatalf("%.0f%% of pure-tone frames were called speech", got*100)
	}
}

func TestAWronglySizedFrameIsRefused(t *testing.T) {
	// Silero is only meaningful on its own window. Quietly padding or trimming
	// would return a number that looks fine and means nothing.
	if _, err := detector(t).Probability(make([]int16, Window-1)); err == nil {
		t.Fatal("a short frame was accepted")
	}
}

func TestResetClearsTheRecurrentState(t *testing.T) {
	d := detector(t)
	speech := readWAV(t, "testdata/speech.wav")

	first, err := d.Probability(speech[:Window])
	if err != nil {
		t.Fatal(err)
	}
	// Run the model well past the point where its state reflects the audio.
	share(t, d, speech)

	d.Reset()
	again, err := d.Probability(speech[:Window])
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(first-again)) > 1e-6 {
		t.Fatalf("after Reset the same frame scored %v, was %v", again, first)
	}
}

// readWAV reads a 16 kHz mono 16-bit file. Deliberately minimal: the fixtures
// are written by the test suite itself, so there is no format to negotiate.
func readWAV(t *testing.T, path string) []int16 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const header = 44
	if len(raw) <= header {
		t.Fatalf("%s is too short to hold audio", path)
	}
	body := raw[header:]
	out := make([]int16, len(body)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(body[2*i:]))
	}
	return out
}
