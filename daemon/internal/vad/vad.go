// Package vad decides whether a 32 ms frame of audio contains human speech.
//
// The model is Silero rather than WebRTC's. WebRTC's detector was built to find
// silence and is weak on music — at a 5% false positive rate Silero makes about
// four times fewer errors — and the daemon's entire job is ignoring sounds that
// are not a conversation. The price is a 41 MB ONNX runtime shipped beside the
// binary, which is the right corner not to cut.
package vad

import (
	"fmt"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/dmykolen/meetings-transcript-and-diarize/daemon/internal/bundled"
)

const (
	// Window is the hop Silero scores at 16 kHz: 512 samples, 32 ms. Callers
	// feed exactly this much.
	Window = 512

	// context is the twist that is easy to miss. The model does not score the
	// 512 samples on their own — it wants the last 64 samples of the previous
	// hop in front of them, and is fed 576 at a time. Silero's Python wrapper
	// does this silently, so nothing documents it; feed it a bare 512 and real
	// speech scores 0.003 instead of 1.0, which looks like a broken model
	// rather than a missing prefix. Measured on this repository's own fixture.
	context = 64
	frameIn = context + Window

	// Speech is the probability above which a frame counts as speech. Silero's
	// own default, and the value its accuracy is usually quoted at.
	Speech = 0.5
)

// libraryName is the ONNX runtime as it is shipped beside the binary. It cannot
// be linked in: it is a 41 MB native library, which is the one place this design
// gives up on "a single file per platform".
var libraryName = func() string {
	switch runtime.GOOS {
	case "darwin":
		return "onnxruntime.dylib"
	case "windows":
		return "onnxruntime.dll"
	default:
		return "onnxruntime.so"
	}
}()

// The runtime is process-wide and may only be initialised once, however many
// detectors are made.
var (
	once    sync.Once
	initErr error
)

func initRuntime() error {
	once.Do(func() {
		lib, err := bundled.Find(libraryName)
		if err != nil {
			initErr = err
			return
		}
		ort.SetSharedLibraryPath(lib)
		initErr = ort.InitializeEnvironment()
	})
	return initErr
}

// Detector is a running Silero session. It is stateful — the model is recurrent,
// so frames must be fed in order — and therefore not safe for concurrent use.
type Detector struct {
	session *ort.AdvancedSession
	input   *ort.Tensor[float32]
	state   *ort.Tensor[float32]
	rate    *ort.Tensor[int64]
	output  *ort.Tensor[float32]
	next    *ort.Tensor[float32]
	closed  bool
}

// New loads the model that ships with the daemon.
func New(sampleRate int) (*Detector, error) {
	if err := initRuntime(); err != nil {
		return nil, fmt.Errorf("onnx runtime: %w", err)
	}
	model, err := bundled.Find("silero_vad.onnx")
	if err != nil {
		return nil, err
	}

	d := &Detector{}
	if d.input, err = ort.NewEmptyTensor[float32](ort.NewShape(1, frameIn)); err != nil {
		return nil, err
	}
	if d.state, err = ort.NewEmptyTensor[float32](ort.NewShape(2, 1, 128)); err != nil {
		return nil, err
	}
	if d.rate, err = ort.NewTensor(ort.NewShape(1), []int64{int64(sampleRate)}); err != nil {
		return nil, err
	}
	if d.output, err = ort.NewEmptyTensor[float32](ort.NewShape(1, 1)); err != nil {
		return nil, err
	}
	if d.next, err = ort.NewEmptyTensor[float32](ort.NewShape(2, 1, 128)); err != nil {
		return nil, err
	}

	options, err := frugal()
	if err != nil {
		d.Close()
		return nil, err
	}
	defer options.Destroy()

	d.session, err = ort.NewAdvancedSession(model,
		[]string{"input", "state", "sr"},
		[]string{"output", "stateN"},
		[]ort.Value{d.input, d.state, d.rate},
		[]ort.Value{d.output, d.next},
		options)
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("load %s: %w", model, err)
	}
	return d, nil
}

// frugal is what makes this usable as a background daemon.
//
// ONNX Runtime defaults to a thread per core and a pool that spin-waits between
// calls. For a 1.8 MB model asked one question every 32 ms that is absurd, and
// it is not theoretical: measured at 237% CPU and 274 MB before this, on a
// machine that was doing nothing else. One thread, no spinning, no parallelism —
// the work is far too small to divide.
func frugal() (*ort.SessionOptions, error) {
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	for _, set := range []func() error{
		func() error { return options.SetIntraOpNumThreads(1) },
		func() error { return options.SetInterOpNumThreads(1) },
		func() error { return options.SetExecutionMode(ort.ExecutionModeSequential) },
		// Without this the pool burns a core waiting for work that comes 31
		// times a second.
		func() error { return options.AddSessionConfigEntry("session.intra_op.allow_spinning", "0") },
		func() error { return options.AddSessionConfigEntry("session.inter_op.allow_spinning", "0") },
	} {
		if err := set(); err != nil {
			options.Destroy()
			return nil, fmt.Errorf("onnx session options: %w", err)
		}
	}
	return options, nil
}

// Probability returns how likely it is that this frame is speech. The frame
// must be exactly Window samples; anything else is a programming error rather
// than a runtime condition, because every buffer in the daemon is built from
// Window in the first place.
func (d *Detector) Probability(frame []int16) (float32, error) {
	if len(frame) != Window {
		return 0, fmt.Errorf("vad: got %d samples, want %d", len(frame), Window)
	}
	// [ last 64 samples of the previous hop | this hop's 512 ]
	samples := d.input.GetData()
	for i, s := range frame {
		samples[context+i] = float32(s) / 32768
	}
	if err := d.session.Run(); err != nil {
		return 0, fmt.Errorf("vad: %w", err)
	}
	copy(samples[:context], samples[frameIn-context:])
	// Silero is recurrent: this frame's output state is the next frame's input.
	copy(d.state.GetData(), d.next.GetData())
	return d.output.GetData()[0], nil
}

// IsSpeech is Probability against the default threshold.
func (d *Detector) IsSpeech(frame []int16) (bool, error) {
	p, err := d.Probability(frame)
	return p >= Speech, err
}

// Reset forgets the conversation so far. Worth doing between recordings, so
// that the tail of one meeting cannot colour the start of the next.
func (d *Detector) Reset() {
	clear(d.state.GetData())
	clear(d.input.GetData()[:context])
}

func (d *Detector) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	if d.session != nil {
		_ = d.session.Destroy()
	}
	for _, t := range []*ort.Tensor[float32]{d.input, d.state, d.output, d.next} {
		if t != nil {
			_ = t.Destroy()
		}
	}
	if d.rate != nil {
		_ = d.rate.Destroy()
	}
	return nil
}
