package media

import (
	"math"
	"testing"
)

// tone is something with structure, so that a correlation between two signals
// means they are related rather than that both are noise.
func tone(n int, hz float64, level float32) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = level * float32(
			0.7*math.Sin(2*math.Pi*hz*float64(i)/Rate)+
				0.3*math.Sin(2*math.Pi*hz*2.7*float64(i)/Rate))
	}
	return out
}

// hiss is noise, for the delay tests. A tone correlates with itself at every
// period, so it matches equally well at a dozen different lags and the search
// picks whichever one rounding favours — which says nothing about the search.
func hiss(n int, level float32) []float32 {
	out := make([]float32, n)
	seed := uint32(20260909)
	for i := range out {
		seed = seed*1664525 + 1013904223
		out[i] = (float32(seed>>9&0xffff)/32768 - 1) * level
	}
	return out
}

// room is what the microphone hears of the far side: quieter, and late.
func room(system []float32, level float32, delay int) []float32 {
	out := make([]float32, len(system))
	for i := delay; i < len(system); i++ {
		out[i] = level * system[i-delay]
	}
	return out
}

func loudest(a []float32) float64 {
	var top float64
	for _, v := range a {
		top = math.Max(top, math.Abs(float64(v)))
	}
	return top
}

// The whole point. The microphone holds the far side coming back off the
// speakers, so adding the two channels put every remote sentence in the mix
// twice — once cleanly and once from the room. The mix must contain it once.
func TestTheFarSideIsHeardOnceAndNotTwice(t *testing.T) {
	n := 3 * Rate
	system := tone(n, 220, 0.35)
	mic := room(system, 0.45, Rate/20) // their voice, 50 ms later, well down

	out := Fold(mic, system)

	// Whatever is left after taking the far side out of the mix must be small:
	// there is nothing else in this recording but their voice and its echo.
	var left, theirs float64
	for i := Rate; i < n; i++ { // skip the first second: the fade starts on the mic
		d := float64(out[i] - system[i])
		left += d * d
		theirs += float64(system[i]) * float64(system[i])
	}
	over := 20 * math.Log10(math.Sqrt(left/theirs)+1e-12)
	if over > -20 {
		t.Errorf("a second copy of the far side survives at %.1f dB under it; want under -20", over)
	}
}

// The other half of the promise. Where the far side is silent the mix is the
// microphone and nothing else — a mix that fixed the echo by removing the
// person in the chair would pass the test above and be useless.
func TestYourOwnVoiceComesThroughUntouched(t *testing.T) {
	n := 2 * Rate
	mic := tone(n, 160, 0.4)
	system := make([]float32, n) // nobody on the call

	out := Fold(mic, system)
	for i := range out {
		if math.Abs(float64(out[i]-mic[i])) > 1e-6 {
			t.Fatalf("the microphone was altered at sample %d: %v became %v", i, mic[i], out[i])
		}
	}
}

// A notification chime is loud on the system channel and lasts a moment. It
// must not take the meeting away from whoever is speaking: the microphone is
// far louder than the chime, and that is what Louder is for.
func TestAChimeDoesNotTakeTheMomentFromYou(t *testing.T) {
	n := 2 * Rate
	mic := tone(n, 160, 0.5)
	system := make([]float32, n)
	copy(system[Rate:], tone(Rate/5, 900, 0.05)) // a fifth of a second, quiet

	out := Fold(mic, system)

	var kept, was float64
	for i := Rate; i < Rate+Rate/5; i++ {
		kept += float64(out[i]) * float64(out[i])
		was += float64(mic[i]) * float64(mic[i])
	}
	if lost := 20 * math.Log10(math.Sqrt(kept/was)); lost < -1 {
		t.Errorf("a chime cost the speaker %.1f dB; want no more than 1", -lost)
	}
}

// Handing the mix from one channel to the other is a crossfade, not a cut. A
// step between two unrelated signals is a click, and a click on every change of
// speaker would be worse than the echo this replaces.
func TestTheHandoverIsNotAClick(t *testing.T) {
	n := 4 * Rate
	mic := tone(n, 160, 0.5)
	system := make([]float32, n)
	copy(system[2*Rate:], tone(2*Rate, 300, 0.5)) // they start talking halfway

	out := Fold(mic, system)

	// The biggest step anywhere in the output, against the biggest step either
	// input takes on its own. A crossfade cannot invent a jump bigger than the
	// signals it is fading between.
	var mixJump, inputJump float64
	for i := 1; i < n; i++ {
		mixJump = math.Max(mixJump, math.Abs(float64(out[i]-out[i-1])))
		inputJump = math.Max(inputJump, math.Abs(float64(mic[i]-mic[i-1])))
		inputJump = math.Max(inputJump, math.Abs(float64(system[i]-system[i-1])))
	}
	if mixJump > inputJump*1.5 {
		t.Errorf("the mix steps by %.4f where its inputs step by at most %.4f — that is a click",
			mixJump, inputJump)
	}
}

// Two signals crossfaded can only be as loud as the louder of them. Clipping
// the result would be distortion on exactly the loudest moments of a meeting.
func TestTheMixNeverClips(t *testing.T) {
	n := 2 * Rate
	mic := tone(n, 160, 0.95)
	system := tone(n, 300, 0.95)

	if top := loudest(Fold(mic, system)); top > 1.0 {
		t.Errorf("the mix reached %.3f; anything over 1 clips", top)
	}
}

// Offset is the fact everything else rests on: the system tap is written into
// the file later than the microphone beside it, and until that is undone no
// echo canceller can converge and a switch cuts a quarter of a second late at
// both ends of every sentence.
func TestOffsetFindsATapThatWasWrittenLate(t *testing.T) {
	const late = 3680 // 230 ms, which is what a real meeting measured
	n := 20 * Rate

	// Their voice, and the microphone's copy of it arriving through the room a
	// few milliseconds later — the ordinary acoustic delay, not the file's.
	played := hiss(n, 0.5)
	mic := room(played, 0.5, Rate/200)

	// The file writes the tap late, so at index i it holds what was played
	// `late` samples ago.
	tap := make([]float32, n)
	copy(tap[late:], played)

	// What has to come out is the gap between the two CHANNELS, which is the
	// file's lateness minus the time the sound spent crossing the room: the
	// microphone already held it 80 samples before the tap channel caught up.
	// That 80 is real acoustics and must survive — undoing it would move the
	// microphone ahead of the sound that made it.
	want := late - Rate/200
	got := Offset(mic, tap)
	if off := got - want; off < -Rate/500 || off > Rate/500 {
		t.Errorf("Offset found %d samples (%.0f ms), want about %d (%.0f ms)",
			got, float64(got)/Rate*1000, want, float64(want)/Rate*1000)
	}
}

// And it must do nothing when there is nothing to do, or every already-correct
// recording would be shifted out of true.
func TestOffsetLeavesAlignedChannelsAlone(t *testing.T) {
	n := 20 * Rate
	system := hiss(n, 0.5)
	mic := room(system, 0.5, Rate/200)

	if got := Offset(mic, system); got > Rate/200 {
		t.Errorf("Offset moved aligned channels by %d samples (%.0f ms)",
			got, float64(got)/Rate*1000)
	}
}
