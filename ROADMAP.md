# Roadmap

What is worth building next, why, and what it is worth. Kept current: items move
to **Done** with a date rather than being deleted, and nothing appears twice —
the ROI ordering below points at the same table rather than repeating it.

Ratings are ●●●●● out of five. **Interest** is whether somebody would want it on
seeing it; **value** is whether it changes how the app is used; **cost** is the
work in this codebase specifically, not in general.

## Where this stands

The app does not lack features. It lacks **surface**.

The summariser already extracts decisions, action items with owner and deadline,
chapters with timestamps, topics and open questions. All of it sits behind a
button, one meeting at a time. And the listener now adds recordings by itself,
so nobody presses that button — an automatic recorder feeding a manual reader.

The Library is a list of filenames like `meeting 2026-08-30 22:39.wav`. That is
an archive. It answers "what do I have" and never "what did I commit to, and
what changed".

Everything in the table below is aimed at one of those two problems.

## The work

| # | Item | Interest | Value | Cost | Status | Notes |
|---|------|:--------:|:-----:|:----:|--------|-------|
| 1 | **Summarise on completion** — the worker does it, nobody asks | ●●●●○ | ●●●●● | ●○○○○ | Done in `standalone` | One hook in `worker.run`. Without it the listener just piles up untouched files |
| 2 | **Name meetings from their summary** instead of `meeting 2026-08-30 22:39.wav` | ●●●○○ | ●●●●● | ●○○○○ | Done in `standalone` | One field. Turns the Library from a file list into a list of subjects |
| 3 | **Notification when a meeting is ready** — "3 action items" | ●●●○○ | ●●●●○ | ●○○○○ | Planned | `osascript` from the daemon, ~15 lines. Clicking opens the transcript |
| 4 | **Chapter navigation** in the player — click a topic, jump to it | ●●●○○ | ●●●●○ | ●○○○○ | Done in `standalone` | `Chapter.start` already exists in every summary and is used nowhere |
| 5 | **Action-item inbox** across all meetings, with done/undone | ●●●●○ | ●●●●● | ●●○○○ | Done in `standalone` | `ActionItem` already carries task, owner and due. Needs a query and a view |
| 6 | **A "Today" screen** instead of a bare list | ●●●●○ | ●●●●● | ●●○○○ | Done in `standalone` | The first thing you see should answer "what did I miss" |
| 7 | **Backfill summaries** for existing recordings, and `POST /jobs/{id}/reindex` | ●●○○○ | ●●●○○ | ●○○○○ | Planned | Also fixes `AIA-planning-20260826`, indexed without vectors |
| 8 | **Export** — SRT, VTT, DOCX, Markdown, copy-as-notes | ●●○○○ | ●●●●○ | ●●○○○ | Markdown done in `standalone` | The dullest item here and the most often wanted |
| 9 | **Search filters** — speaker, date range, kind | ●●○○○ | ●●●○○ | ●●○○○ | Planned | 28 recordings can still be scanned by eye; 200 cannot |
| 10 | **Weekly digest** — what was decided, what you owe, what stalled | ●●●●● | ●●●●● | ●●●○○ | Planned | Needs 1, 2 and 5 first |
| 11 | **Speaker profile** — everything Olena said about the migration | ●●●●○ | ●●●○○ | ●●○○○ | Named voices done in `standalone` | Voices already knows who is who; this is a query, not a mechanism |
| 12 | **Delivery to Slack or email** — the summary arrives, you do not fetch it | ●●●●○ | ●●●●● | ●●●○○ | Planned | The value of a meeting is not in going to look for it |
| 13 | **Meeting series** — group recurring ones, carry unfinished items forward | ●●●●● | ●●●●○ | ●●●●○ | Planned | "We have discussed this three times and still not decided" |
| 14 | **Ask with memory** — follow-ups, and "what changed since last time" | ●●●●● | ●●●●○ | ●●●●○ | Planned | `/ask` is one-shot today, with no thread |
| 15 | **Live assistant** — questions aimed at you, terms, prompts as it happens | ●●●●● | ●●●○○ | ●●●●● | Idea | The most impressive and the most expensive. Live latency is ~6 s; whether that is usable has to be measured on a real meeting, not guessed |

### Why Parakeet, in numbers — now available as `MT_ASR_BACKEND=parakeet`

Ten of the twelve recent meetings are in Ukrainian, two in Russian. The model in
use, `large-v3-turbo`, is `large-v3` with the decoder cut from 32 layers to 4 —
a 1–2% WER cost on well-resourced languages and far more on the rest.

Published, on FLEURS:

| | Ukrainian | Russian |
|---|---|---|
| Parakeet TDT 0.6B v3 | **5.10%** | **3.00%** |
| Whisper large-v3 | 12.52% | 4.04% |

Measured here, on three minutes cut from a real Ukrainian meeting
(`meeting 2026-09-01 13:31`):

| Model | Time | ×realtime | Words | Repeated lines |
|---|---|---|---|---|
| large-v3-turbo | 3.5 s | 50.8× | 199 | 2 |
| large-v3 | 16.3 s | 11.0× | 133 | 9 |
| **parakeet-tdt-0.6b-v3** | **2.3 s** | **78.0×** | 184 | **0** |

Whisper looped on a quiet stretch — "Сто літу" five times over — while Parakeet
emitted nothing there and transcribed the technical passage either side of it
that both Whisper models lost entirely.

The architecture is why. Whisper decodes autoregressively and will invent fluent
text when there is nothing to hear; a transducer emits a blank per frame and
stays quiet. That makes the hallucination class structural rather than something
to filter afterwards — and the confidences on the invented rows were 0.75–0.90,
so filtering by confidence was never going to work.

It is **added, not substituted**: `auto` still picks CTranslate2 or MLX exactly as
before, and Parakeet is opted into with `MT_ASR_BACKEND=parakeet`. It is Apple
Silicon only (`uv sync --extra parakeet`) and needs a Linux path before it could
ever be the default anywhere.

### By ROI

Value per unit of work, highest first: **1, 2, 3, 4, 5, 6, 7, 8, 9, 10**.

Items 1–4 are one evening together, and they change how the app feels more than
anything below them. Start there. Items 11–15 are worth doing, but none of them
is worth doing before the app can tell you what happened yesterday.

### By how interesting it is

**10, 13, 14, 15, 5, 11, 12, 1, 3, 4** — but interest is the weaker axis. The
listener already makes the app do something no one else's does; what it cannot
yet do is show the result.

## Known problems

| Item | Severity | Status | Notes |
|------|----------|--------|-------|
| `test_a_recording_being_worked_on_is_refused_until_its_worker_goes_quiet` races the live worker | Low | Open | Fails under CPU load only. The app is correct; the test is racy. See the spawned task |
| Windows and Linux system audio written but never run | Medium | Open | WASAPI loopback and the PipeWire monitor compile but have not executed on their own platform. cgo means each needs its own build machine, as does signing — waiting on a CI matrix |
| No authentication or rate limiting on the API | Medium | Open | Loopback-only by default, which is why this is not urgent |
| Comb filtering when recording without headphones | Medium | Fixed in `standalone` | It was not a few milliseconds: the mixer held the system channel half a second behind, so every remote sentence was heard twice on playback. The bound is now 125 ms and the mono mix takes the tap rather than averaging. Still open in the Python edition |
| Two copies of the signing certificate in the keychain | Cosmetic | Open | Harmless: the build picks the lowest fingerprint deterministically. Tidy up in Keychain Access when convenient |
| One person becomes several in a long recording | Medium | Partly fixed | The microphone side is now one speaker by construction, which fixed a solo recording going from 3 speakers to 1. The far side of a call still relies on the clusterer, and its cluster voiceprints do not separate: six real people sit at 0.49–0.80, one person split in three at 0.59–0.81. Needs a better embedding model |
| Several people in one room are one voice | Low | Open | They arrive on one channel and nothing can tell them apart from it. The Python edition has the same limitation |
| `open` does not pass the environment, so `OPENAI_API_KEY` is invisible to the app | Low | Handled | The key belongs in Settings. The app now says so at startup and on the screens that need it, instead of silently skipping summaries |

## Done

**2026-09-04 — the echo, and what it was hiding.** Playing a recording back gave
every sentence somebody else said twice over. The cause was ours: the mixer let
the system channel run half a second behind the microphone and only ever trimmed
it at that bound, so the backlog sat there for the whole meeting. Worse, the
mono mix for the models was the *average* of the two channels — a signal summed
with a room recording of itself, which is comb filtering. Fixing the mix fixed
three things at once: playback has one copy of everything, a real 26-minute
meeting went from 11 speakers to 4 with the owner's own 19 minutes in one place,
and Parakeet — rejected a week ago on the strength of producing three words in
three minutes — turned out to have been choking on that same signal. Also:
Ukrainian is now named rather than detected, so a meeting with a few borrowed
words stops coming back written in Russian.

**2026-09-01 — the desktop app grows up.** Any format the machine can read, not
just WAV: macOS opens m4a, mp3, aac, flac and the audio of an mp4 through
afconvert with nothing installed, and an ffmpeg the app fetches on first run
covers webm, ogg and mkv — six containers pinned by test. **Named voices**: a
voiceprint per speaker is kept with each recording, so naming somebody once
teaches the app their voice and they arrive named from then on; verified on a
real meeting, where the name came back by itself on the next run and the
summary then used it as the owner of an action item. **Analytics** — talk time,
silence, overlap, pace, who asked the questions, how evenly the floor was
shared, and the shape of the hour as a bar per slice. A **Summarise** button, so
a meeting can be read back after a key is added or after the speakers are named.
Pressing Record makes a *note*, and it turns into a meeting on its own if
somebody else starts talking. An app icon, and a disk image with a laid-out
window.

Two things nothing asked for. **The live transcript**: the meeting is written
down as it happens, from the utterances the detector was already cutting, and
the queue stands aside while a meeting is being recorded so the two do not fight
over the cores. And **Today**, the screen that answers what a pile of
transcripts knows and a person does not — what was decided, what is still owed,
what is past its date, and which questions have been asked in more than one
meeting without ever being answered. All of it local, and none of it needs a
key.

**2026-09-01 — the desktop app, one file.** `standalone/` is the whole product
as a single signed macOS bundle: Wails v3 around React 19, whisper.cpp on Metal
and sherpa-onnx for speakers, SQLite with FTS5, and no server, no Python, no
Docker. It creates `~/MeetingTranscriber/` on first launch and downloads its
583 MB of models into it, resumably. It listens on its own — the daemon's ring,
detector and capture ported across, with Silero driven through sherpa-onnx
rather than a second ONNX runtime. `make dmg` produces a 34 MB disk image to
hand somebody who has never opened a terminal. Verified end to end: a real
meeting heard through the Core Audio tap, replayed 46 s from before it was
noticed, transcribed at 12× realtime with two speakers separated, and the app
run from the disk image outside the build tree. See
[standalone/PLAN.md](standalone/PLAN.md).

The interface answers most of the surface problem below: the Library shows
subjects rather than filenames, chapters are clickable, the to-do list spans
every meeting with checkboxes that stick, search highlights the passage, and the
listener's state — listening, recording, wrapping up — is visible at all times
with ⌘R to start one by hand.

**2026-08-31 — the always-on listener.** A Go daemon that hears meetings
happening and hands them to the app; nobody presses record. Microphone and
system audio as two channels of one file, Silero deciding what is speech, a
ten-minute memory buffer so recordings begin five minutes before anything
noticed. Menu bar item, manual *Record now*, spool that survives the app being
down. `make install` sets up both halves to start at login. See
[DAEMON.md](DAEMON.md).

**2026-09-01 — Parakeet TDT v3 as an optional engine.** `MT_ASR_BACKEND=parakeet`
switches transcription to NVIDIA's Parakeet, which is 2.5× more accurate on
Ukrainian and, being a transducer, cannot hallucinate over silence the way
Whisper does. Added rather than substituted — `auto` behaves exactly as before.
Sub-word tokens are merged back into words so diarization still aligns, and the
language is read off the alphabet because Parakeet identifies it internally and
then does not say. Verified through the whole pipeline, diarization included, on
a real Ukrainian meeting.

**2026-09-01 — silence trimmed at both ends.** Real meetings were starting with
up to five minutes of an empty room (first word at 4.6, 4.7 and 5.0 minutes into
three of them) and ending with up to 24 minutes of it, because the whole preroll
was replayed whether anybody had been talking in it and the quiet that ends a
recording was written before the recording was known to be over. The ring now
remembers which frames held speech and replays from the first of them, and quiet
frames are held back and dropped unless the talking resumes. A pause inside a
meeting is still kept; only the silence around it goes.

**2026-08-31 — stable code signing.** `make cert` creates a local self-signed
certificate so the microphone permission survives rebuilds, instead of macOS
asking again after every build. Verified by rebuilding and diffing the
designated requirement: the binary hash changes, the requirement does not.
