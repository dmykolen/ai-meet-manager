# standalone — working plan

The single-binary edition. Nothing outside `standalone/` is ever modified; that
is checked with `git status --porcelain -- app/ web/ tests/ daemon/ pyproject.toml`.

## Done

- [x] `internal/home` — `~/MeetingTranscriber/`: config, db, recordings, models, logs
- [x] `internal/models` — first-run download, 583 MB, resumable, progress
- [x] `internal/engine` — whisper.cpp (Metal) + sherpa-onnx diarization, verified
      at 19.2× realtime on a real meeting
- [x] `internal/insights` — summary and Q&A over `openai-go/v3` Responses API,
      verified live on a 153-turn meeting
- [x] `Makefile` — vendors and builds whisper.cpp at a pinned commit

- [x] `internal/store` — SQLite with FTS5 search, 13 tests
- [x] `internal/media` — WAV natively, afconvert, then ffmpeg; 12 tests
- [x] `internal/library` — the queue: decode → engine → store → summarise, 8 tests

- [x] `internal/audio` — microphone + system capture, ported from `daemon/source`
- [x] `internal/listen` — ring, detector, recorder; Silero through sherpa-onnx
      rather than a second ONNX runtime, 12 tests
- [x] `internal/service` — the bound surface: 25 methods, typed TS bindings
- [x] `main.go` — Wails v3, models loaded behind the window rather than in front
- [x] `frontend/` — React 19 + Vite 7 + Tailwind v4 + Motion + lucide
- [x] Six screens — Today, Library, Transcript, Search, To do, Ask, Settings —
      all driven by hand in a browser
- [x] Import from the UI, through the native file picker
- [x] `make bundle` / `make dmg` — signed .app, dylibs carried inside it, 34 MB disk image
- [x] Verified end to end: 583 MB downloaded, a real meeting captured through the
      system tap, replayed 46 s from before it was noticed, transcribed at 12×
      realtime with two speakers separated, and the app run from the DMG copy
      outside the build tree

- [x] Any format — WAV here, everything Core Audio reads through afconvert,
      the rest through an ffmpeg the app fetches. Six containers pinned by test.
- [x] Named voices — a voiceprint per speaker, kept per recording; naming
      somebody once teaches the app their voice. Verified on real audio: the
      name came back by itself on the next run.
- [x] Analytics — talk time, silence, overlap, pace, questions asked, how evenly
      the floor was shared, and the shape of the hour. Computed from the rows,
      so it follows a rename with no migration.
- [x] Summarise on demand, and a summary that fills overview, topics, decisions,
      owners and deadlines. Verified against a real Ukrainian meeting.
- [x] Record now makes a note, not a meeting — and it becomes a meeting on its
      own if somebody else starts talking.
- [x] App icon. The disk image lays its window out too, but only on a machine
      that has granted Finder automation — see the note below.
- [x] **Live transcript** — the meeting written down as it happens, from the
      utterances the detector already cuts. The queue stands aside while a
      meeting is being recorded so the two do not fight over the cores.
- [x] **Today** — what happened, what was decided, what is still owed, what is
      past its date, and what has been asked in more than one meeting without
      an answer. All local, no key needed.

- [x] **Who said it, using the channels.** The microphone side is the person
      sitting here and the system side is everybody else, and the app was
      throwing that away. A recording with a silent system channel is now never
      diarized at all, and in a call everything markedly louder on the
      microphone is folded into one speaker. Measured: seven minutes of one
      voice went from three speakers to one, and a 64-minute meeting from nine
      labels to seven with the owner's own turns finally in one place.
- [x] Retention — audio older than `keep.audio_days` is deleted daily and on
      demand; the transcript, summary and analytics always stay.
- [x] Playback — a range-served route inside the app, a player under the header,
      click a row to hear it, and the row being spoken is lit.
- [x] Search by meaning — passages embedded at 512 dimensions, cosine in memory,
      blended with the keyword index. Verified on 574 real passages: "who owns
      testing quality" finds Ukrainian passages about tests and metrics where
      keyword search returns nothing.

- [x] **The echo.** Playing a recording back gave every remote sentence twice,
      once through the room and once from the tap. Measured: the microphone led
      the tap by 495 ms for a whole meeting, because the mixer's backlog bound
      was half a second and was only ever trimmed *at* the bound. The bound is
      now 125 ms, and the mono mix takes the tap whenever the tap has anything
      instead of averaging the two — averaging was summing a signal with a room
      recording of itself. Correlation with the clean tap went from 0.605 to
      0.898, and one real meeting went from 11 speakers to 4.
- [x] Ukrainian by default, and named rather than detected. A summary is written
      in the configured language whatever the transcript mixes in.
- [x] Ten voiceprints per person, up from eight
- [x] Delete on hover in the Library, and transcribe-again inside a meeting
- [x] "This is me" — one enrolment for the person holding the laptop
- [x] Parakeet, choosable in the settings, downloaded only when chosen

## Now

- [ ] The far side of a call is still whatever the clusterer says. Measured:
      cluster voiceprints of six genuinely different people sit at 0.49–0.80,
      the same range as one person split in three, so a merge pass on them
      cannot work. Fixing it needs a better embedding, not a better threshold.
- [ ] Several people in one room are one voice, because they arrive on one
      channel. The same limitation the Python edition has.

## Then

- [ ] Windows and Linux: `system_other.go` is written and has never been run
- [ ] Export to SRT/VTT/DOCX — `Markdown` is the only export today

## Notes worth keeping

- **The two VAD models are not the same one.** `vad.bin` is ggml, lives inside
  whisper.cpp, and answers about a finished file — without it Whisper wrote
  "Дякую." over the first ninety seconds of silence. `silero_vad.onnx` runs
  through sherpa-onnx and answers about the last 32 ms, which is what decides a
  meeting is happening. Neither replaces the other.
- **Silero through sherpa, not a second runtime.** The daemon drives Silero with
  `yalue/onnxruntime_go` and ships a 41 MB `onnxruntime.dylib` beside the binary.
  sherpa-onnx is already linked in for the speaker models and carries its own
  runtime, so this edition asks it instead — one dependency fewer, and its
  wrapper handles the 64-sample context that made a bare 512-sample hop score
  0.003 on real speech.
- **The bundle carries its dylibs.** Go links `libsherpa-onnx-c-api.dylib` and
  `libonnxruntime.dylib` through an rpath into the Go module cache, which is
  useless on anybody else's machine. `make bundle` copies both into
  `Contents/Frameworks` and rewrites the rpath; `otool -l` should show
  `@executable_path/../Frameworks` and nothing else.
- **`open` does not pass the environment.** `OPENAI_API_KEY` reaches a process
  started from a shell and not one started from Finder, so the key belongs in
  Settings. The app now says so at startup and on the screens that need it,
  rather than quietly skipping every summary.
- **The live transcript was almost free.** The detector already cuts the audio
  where somebody stopped talking, because it has to know that anyway. Those
  finished segments are exactly what a transcriber wants to be fed, so the
  feature is one goroutine and a bounded list — no second VAD, no second model,
  no new audio path.
- **The disk image window needs Finder.** Setting a background and icon
  positions is only possible through Finder, which needs a one-time Automation
  permission for whatever runs `make`. Without it `make dmg` still produces a
  working image, just an unstyled one, and says so.
- **The channels are the diarizer.** Left is the microphone, right is the
  system tap, and that says more about who is speaking than any clustering
  algorithm can work out from the sound. Measured before touching it: on a
  seven-minute recording of one person, sherpa returned 7 speakers at a
  clustering threshold of 0.7, 4 at 0.9 and 1 at 1.2 — there is no threshold
  that is right for both that recording and a real meeting. The channel is not
  a guess.
- **A merge pass on voiceprints was tried and rejected.** Embedding each
  cluster and joining the ones that sound alike collapses a solo recording to
  one speaker at every threshold — and collapses six real people to one or two
  at the same thresholds, because their cluster voiceprints sit at 0.49–0.80
  and one person split in three sits at 0.59–0.81. The ranges overlap
  completely. Pooling per segment instead of splicing the audio made it worse,
  not better. Do not re-litigate without new numbers.
- **Averaging two channels was the bug behind three others.** Without headphones
  the microphone hears the far side coming out of the speakers, so the average
  of the two channels is a signal summed with a delayed copy of itself. That is
  an audible echo on playback, comb filtering for Whisper, and a smeared energy
  comparison for speaker attribution. `media.Fold` takes the tap whenever the
  tap is active and the microphone otherwise. Measured on a real meeting:
  correlation with the clean tap 0.605 averaged against 0.898 folded, and the
  averaged signal was *quieter* than either channel because the copies cancel.
- **Parakeet was rejected on a broken measurement.** Fed the averaged signal it
  produced three words in three minutes, which is what put it in the "rejected"
  column. On the folded signal it produces 161. It is still not the default:
  Whisper found 320 words on the same three minutes, in Ukrainian, where
  Parakeet wrote Russian — sherpa's transducer takes no language argument, so
  there is nothing to set that stops it. It is in the settings so the numbers
  can be checked rather than believed.
- **Run it from the bundle, never as a bare binary.** macOS attributes the
  microphone and system-audio grants to the responsible application; started
  from a terminal, the grant goes to the terminal.

## Open questions

- Agents SDK: using `openai-go/v3` behind an `Ask`/`Structured` seam. The Go port
  of the Agents SDK is v0.1.0 and five months stale. Revisit when the roadmap's
  agentic items arrive.
