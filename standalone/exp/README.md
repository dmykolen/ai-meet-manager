# Experiments

Nothing here ships. Every experiment is one directory with one `main.go`, run by
hand, printing far more than a program normally should — the point is to see the
situation, not to be tidy about it.

    go run ./exp/01_echo    "$HOME/MeetingTranscriber/recordings/meeting 2026-09-09 11-58.wav"
    go run ./exp/02_speakers 74

The rule: an idea is only allowed into `internal/` after it has been measured
here against a real recording *and* against the edge cases that broke the last
attempt. Two rounds of "fixed it" that were not fixed is why this folder exists.

`truth/` holds ground truth the owner gave by ear. It is the only thing in this
repository that knows the right answer.

## What is being chased

**Echo.** The recordings are stereo: left is the microphone, right is the system
tap. Without headphones the microphone also picks up the far side coming out of
the speakers, so the two channels hold the same voice twice, a room delay apart.
Folding them to mono by ducking the microphone was the first attempt; it leaves
a second copy audible on real material.

**Voices.** sherpa-onnx splits the audio into speakers, then `store.Same` merges
clusters that match an enrolled voiceprint. On meeting 74 that merged a speaker
nobody has enrolled into somebody who is — the one mistake the design says must
never happen, because it cannot be undone afterwards.

## Findings so far

**The two channels are not aligned.** The microphone leads the system tap by a
steady ~230 ms for the whole of meeting 74 — the room appears to hear the far
side before it was played, which cannot happen acoustically and means audiotee's
stream is interleaved late. An echo canceller models a *causal* response, so an
echo sitting behind the reference is outside the model: SpeexDSP removed 0.0 dB
and the app's own NLMS removed 0.0 dB, and neither was broken.

Shift the tap 230 ms earlier and SpeexDSP removes 8.1 dB. The sweep peaks
cleanly (0.0 → 3.3 → 8.1 → 6.6 → 5.2), which is what a real alignment looks
like.

`internal/media.roomLag` currently forbids a negative answer, on the reasoning
that sound cannot be heard before it is played. That reasoning is right about
rooms and wrong about this file, and it is one of the things keeping the echo
in: it forces the search into the half-plane the answer is not in.

The experiments needing SpeexDSP carry `//go:build speex`, so `go build ./...`
and `go test ./...` stay clean on a machine without `brew install speexdsp`.
Run them with `go run -tags speex ./exp/05_aligned <file>`.

## Findings, second round

**Whisper repeats itself, and that is not echo.** Meeting 74 held "Данію." ten
times in a row. `max_text_ctx = 0` — condition_on_previous_text off — removes
every repetition and runs a quarter faster. A temperature fallback and beam
search were measured on the same audio and added nothing. Parakeet does not loop
but produced 11 rows for 23 minutes, largely nonsense; closed.

**Speakers: nothing works alone, one pair works.** Nine configurations against
the owner's ear (exp/out/F-speakers-74.txt). Every one on the mixed mono folds
the stranger into the enrolled colleague, including Rev's reverb segmentation
(also 5x slower, and non-production licensed) and both alternative embedding
models. Diarizing the SYSTEM CHANNEL ALONE with the 3D-Speaker zh_en embedding
is the only thing that keeps them apart — and it is faster, because the owner is
not in the audio being clustered at all.

Both are now in `internal/`. pyannote community-1, which the field considers the
current best, has no ONNX export in sherpa-onnx; only segmentation-3.0 and
reverb are available.

## community-1, measured

pyannote community-1 — the best open-source diarizer there is, and the one the
Python edition of this project uses — scored on the same meeting and the same
question as the nine sherpa configurations:

    on the MIXED audio   the stranger folded into the colleague.  Fails.
    on the SYSTEM TAP    kept apart.  Passes.

Which is exactly what sherpa does. The channel mattered and the model did not:
the app is already where it needs to be, and the missing ONNX export of
community-1 has stopped being a problem worth solving.

It is better calibrated — five speakers on the tap against sherpa's fourteen —
but the app splits on purpose and rejoins by voiceprint, so that is not a fault
on this side. Speed on MPS was 35-42x realtime against sherpa's 26x.

The cost of using it would be PyTorch and a Python runtime, which is the whole
thing the single-binary edition exists to avoid. Now there is a measurement
saying that price buys nothing here.
