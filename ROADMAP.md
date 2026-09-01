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
| 1 | **Summarise on completion** — the worker does it, nobody asks | ●●●●○ | ●●●●● | ●○○○○ | Planned | One hook in `worker.run`. Without it the listener just piles up untouched files |
| 2 | **Name meetings from their summary** instead of `meeting 2026-08-30 22:39.wav` | ●●●○○ | ●●●●● | ●○○○○ | Planned | One field. Turns the Library from a file list into a list of subjects |
| 3 | **Notification when a meeting is ready** — "3 action items" | ●●●○○ | ●●●●○ | ●○○○○ | Planned | `osascript` from the daemon, ~15 lines. Clicking opens the transcript |
| 4 | **Chapter navigation** in the player — click a topic, jump to it | ●●●○○ | ●●●●○ | ●○○○○ | Planned | `Chapter.start` already exists in every summary and is used nowhere |
| 5 | **Action-item inbox** across all meetings, with done/undone | ●●●●○ | ●●●●● | ●●○○○ | Planned | `ActionItem` already carries task, owner and due. Needs a query and a view |
| 6 | **A "Today" screen** instead of a bare list | ●●●●○ | ●●●●● | ●●○○○ | Planned | The first thing you see should answer "what did I miss" |
| 7 | **Backfill summaries** for existing recordings, and `POST /jobs/{id}/reindex` | ●●○○○ | ●●●○○ | ●○○○○ | Planned | Also fixes `AIA-planning-20260826`, indexed without vectors |
| 8 | **Export** — SRT, VTT, DOCX, Markdown, copy-as-notes | ●●○○○ | ●●●●○ | ●●○○○ | Planned | The dullest item here and the most often wanted |
| 9 | **Search filters** — speaker, date range, kind | ●●○○○ | ●●●○○ | ●●○○○ | Planned | 28 recordings can still be scanned by eye; 200 cannot |
| 10 | **Weekly digest** — what was decided, what you owe, what stalled | ●●●●● | ●●●●● | ●●●○○ | Planned | Needs 1, 2 and 5 first |
| 11 | **Speaker profile** — everything Olena said about the migration | ●●●●○ | ●●●○○ | ●●○○○ | Planned | Voices already knows who is who; this is a query, not a mechanism |
| 12 | **Delivery to Slack or email** — the summary arrives, you do not fetch it | ●●●●○ | ●●●●● | ●●●○○ | Planned | The value of a meeting is not in going to look for it |
| 13 | **Meeting series** — group recurring ones, carry unfinished items forward | ●●●●● | ●●●●○ | ●●●●○ | Planned | "We have discussed this three times and still not decided" |
| 14 | **Ask with memory** — follow-ups, and "what changed since last time" | ●●●●● | ●●●●○ | ●●●●○ | Planned | `/ask` is one-shot today, with no thread |
| 15 | **Live assistant** — questions aimed at you, terms, prompts as it happens | ●●●●● | ●●●○○ | ●●●●● | Idea | The most impressive and the most expensive. Live latency is ~6 s; whether that is usable has to be measured on a real meeting, not guessed |

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
| **Whisper hallucinates over the quiet tail of every recording** | Medium | Open | The listener records three minutes of silence at the end of every meeting *by design* — that is how it decides the meeting is over — and Whisper invents rows over it ("and you can see the next video", repeatedly). The fix belongs in the daemon, which knows exactly how long the quiet ran: trim the tail before spooling. Server-side `_hallucinated()` does not catch these |
| Comb filtering when recording without headphones | Low | Open | Both channels carry the same audio a few ms apart. Fix is a cross-correlation check, ~15 lines |
| Two copies of the signing certificate in the keychain | Cosmetic | Open | Harmless: the build picks the lowest fingerprint deterministically. Tidy up in Keychain Access when convenient |

## Done

**2026-08-31 — the always-on listener.** A Go daemon that hears meetings
happening and hands them to the app; nobody presses record. Microphone and
system audio as two channels of one file, Silero deciding what is speech, a
ten-minute memory buffer so recordings begin five minutes before anything
noticed. Menu bar item, manual *Record now*, spool that survives the app being
down. `make install` sets up both halves to start at login. See
[DAEMON.md](DAEMON.md).

**2026-08-31 — stable code signing.** `make cert` creates a local self-signed
certificate so the microphone permission survives rebuilds, instead of macOS
asking again after every build. Verified by rebuilding and diffing the
designated requirement: the binary hash changes, the requirement does not.
