# Working on this project

Read this before changing anything. It records how the owner wants this codebase
built, and the traps that have already cost a round of rework.

The owner writes in Ukrainian and expects replies in Ukrainian. The code, comments,
documentation and UI stay in English.

## The one rule that overrides the others

**Less code, fewer moving parts, easier to read.** Every implementation decision is
judged against that before it is judged against anything else. When two designs both
work, the shorter and more obvious one wins — even if the other is more "architectural".

## Code style

- Write the simplest implementation that fully solves the problem. Not a sketch of one,
  and not a framework for one.
- Reach for a mature library instead of writing the thing yourself. If pyannote already
  returns the overlap-free annotation, do not write an overlap solver.
- No abstractions for requirements that do not exist yet. No factories, registries,
  wrappers, interfaces, base classes or indirection layers added "for later".
- Do not split simple logic across files or functions without a clear benefit. A helper
  used once, in one place, usually belongs inlined.
- Keep functions, modules, endpoints and data models small and obvious.
- Prefer direct, idiomatic Python and JavaScript over clever code.
- Dependencies are deliberate. Each one must earn its place; say what it buys.
- Avoid duplication, but do not invent an abstraction to remove three repeated lines.
- Comments explain *why*, never *what*. A comment restating the code is noise.

## How to work

- **Research before choosing a technology.** Never state a current fact from memory —
  library versions, model names, API shapes, what is maintained. Check the docs.
  DeepWiki and Context7 MCP for framework internals, web search for the ecosystem.
- **Verify assumptions against the installed version**, not against a blog post. Import
  the package and read the signature.
- **Work the whole task through.** Research, implement, test, simplify, then the UI —
  without stopping to report after each step.
- **Simplify before declaring done.** Re-read the diff looking for unnecessary code,
  redundant abstractions, avoidable complexity and over-fragmented modules.

## How to test — the part that has gone wrong before

- **Stub the model, never the code around it.** A stub that replaces
  `pipeline.transcribe` hides every bug in `pipeline.transcribe`. Replace only
  `engines.asr` / `engines.diarizer` and let the real code run. This exact mistake
  shipped a crash that made Live unusable.
- **Fakes must reject what the real library rejects.** The Whisper fake raises on an
  invalid language code, because the real one does. That is what catches the class of
  bug the stub would otherwise hide.
- **Test with a real `.env` copied from `.env.example`**, not with an empty environment.
  Blank values are how the `MT_LANGUAGE=` and `MT_MODEL_CACHE=` bugs got in.
- **Drive the UI by hand in a real browser** — file pickers, buttons, microphone,
  WebSocket — and collect screenshots as evidence. Record every page and console error;
  a JavaScript error is a failure even when the check passes.
- **Anything found gets fixed, then the whole verification starts over from a clean
  database.** Not a patch and a partial re-run.
- Alpine note: `x-show` hides an element but keeps evaluating everything inside it. Use
  `<template x-if>` whenever the contents dereference something that may be absent.
- Playwright's bundled Chromium has no H.264 or AAC. Prove media paths with WebM/VP8.

## Operational expectations

These are requirements, not polish:

- **The server starts in seconds** (measured: 4–6, down from 122). What made it slow was
  import time, not the server. `app.engines`, `app.pipeline`, `app.live`, the Agno agents
  and the OpenAI client are therefore imported lazily — inside `worker.run`, the WebSocket
  handler, `voices.embed` and the agent factories. Do not move any of them back to module
  level in something `app.main` reaches at startup, or boot returns to two silent minutes.
  Base torch still arrives transitively (`app.media` → faster-whisper → ctranslate2) and
  costs about a second; pyannote, lightning and the LLM SDKs are what actually hurt, and
  those stay deferred. Re-measure with `python -X importtime -c "import app.main"`.
- **Live latency has a floor, and it has been measured.** Whisper's encoder runs a
  30-second window whatever you feed it, so every utterance costs the same ~2.5s on CPU
  no matter how short it was. On an M3 Max the median lag from spoken to shown is ~6s.
  Two things got it down from ~8s and both are cheap: the language is detected once per
  session rather than on every phrase (an extra encoder pass, ~1.6s), and live decodes
  greedily (`beam_size=1`, 1.8x faster and measurably no worse). Three obvious-looking
  fixes were measured and rejected — a smaller model (loops on long utterances, ends up
  *slower*), cutting utterances shorter (Whisper hallucinates "Дякую за перегляд!" onto
  short fragments), and batching for live (a wash on one clip). Do not re-litigate any of
  them without numbers.
- **Nothing happens silently.** Startup logs the database, media directory, model cache
  and its size, role and both devices. Model loading logs what is being fetched and
  whether it came from the cache or was downloaded, with the size and elapsed time. Every
  phase of a job logs its start and its pace — decode, trim, transcribe, diarize, done —
  because a job that stops needs to say *where* it stopped. Timestamps carry
  milliseconds. `uvicorn.access` and `httpx` are turned down to WARNING: the UI polls
  while a job runs and its requests bury everything worth reading. `MT_ACCESS_LOG=true`
  brings the request log back.
- **A worker thread must outlive anything one job can do to it.** `run` swallows what a
  job raises, but the loop around it — claiming, committing the final row — used to be
  able to kill the thread. With `MT_CONCURRENCY=1` that silently stops the whole queue
  and leaves the job `running` for ever with nobody to take it over. `_loop` now catches
  and carries on.
- **Models are downloaded once.** `MT_MODEL_CACHE` (or `HF_HOME`) must point at
  something persistent; the Docker image keeps it on a named volume.
- **Everything is kept and browsable.** Transcripts, summaries, analytics and notes live
  in the database indefinitely and are reachable from the Library tab. Media files live
  on the filesystem for `MT_KEEP_MEDIA_DAYS`.
- **Silence never reaches the models.** One VAD pass trims it, and timestamps are mapped
  back onto the original recording.
- **The machine picks the Whisper, not the config.** `engines.backend()` sends CUDA to
  CTranslate2 (float16 there is fastest and best proven), Apple Silicon to MLX when the
  optional extra is installed (14.6x realtime against 6.5x on the whole meeting), and
  everything else to CTranslate2. MLX has Linux wheels now but nobody has measured them;
  measuring is what would change that line, not preference. `engines.Mlx` wears
  faster-whisper's signature so nothing downstream branches, and `MLX_WEIGHTS` writes the
  repository names out because mlx-community follows no pattern — a derived name found
  `whisper-large-v3-turbo-fp16`, which exists, is not MLX weights, and fails at load.
- **The two models want different hardware.** CTranslate2 speaks CUDA or CPU and nothing
  else, so `device()` is for Whisper. pyannote is PyTorch and reaches Apple Silicon, so
  `torch_device()` is for it — measured at 10.5× realtime on MPS against 1.8× on the same
  CPU, which is the difference between 4 and 22 minutes on a meeting. Anything handing a
  tensor to the embedding model must move it to the model's device and bring the vector
  back, which is what `voices.embed` does.
- **Speed is measured, not assumed.** A 39-minute meeting runs in 5 minutes on an M3 Max
  without speakers and 6 with them. Two settings got it there and both had been left on a
  bad default:
  CTranslate2 takes 4 threads unless told the machine's count, and batched decoding was
  switched off on CPU on the belief it only pays on a GPU — it is worth 2× on both.
  Batching also *requires* its own VAD (`vad_filter=True`) and raises past 30 seconds
  without it, and it returns 30-second blocks rather than sentences, which is why
  `_turns` rebuilds the rows from word timestamps. That same function serves the diarized
  path, because a speaker change is not the only reason to end a row either — diarization
  will otherwise hand back three unbroken minutes of one person. Re-measure before
  changing any of it; `tests/test_api.py` pins the batching contract and
  `tests/test_pipeline.py` the row building.
- **Audio and video are stored on the filesystem**, not in the database or an object
  store. Any container FFmpeg reads is accepted, video included.
- **A tab does its own job, and nothing navigates on the user's behalf.** Library is
  where transcripts are read — the list opens one in place and `← Library` or `Esc`
  goes back. Upload and Live *run* a recording and end on a result card with an
  `Open transcript` button. The previous UI threw the user from Library to Recording
  on every click; do not reintroduce a tab switch the user did not ask for.
- **Every wait is a visible state.** Warming up, listening, N seconds behind, wrapping
  up, saved. A spinner-free pause is a bug, not a quiet moment.
- **One palette, no `dark:` variants.** `web/index.html` defines semantic colours as
  CSS variables twice — light, then under `prefers-color-scheme: dark` — and exposes
  them through `@theme inline`. Utilities are `bg-surface`, `text-soft`, `border-line`.
  Adding a `dark:` class means the token is missing; add the token instead.

## The stack, and why

Chosen after comparing the field; see `RESEARCH.md` for the full comparison.

- Python 3.11+, `uv` for the project and its dependencies
- FastAPI, SQLModel, Pydantic, pydantic-settings
- **faster-whisper** (CTranslate2) — same code on GPU and CPU, INT8 on CPU
- **pyannote.audio 4.x** with `speaker-diarization-community-1` — no cap on speakers,
  returns `exclusive_speaker_diarization` for alignment and speaker embeddings for
  identification. Gated: needs a Hugging Face token.
- **Agno** for the summary and Q&A agents
- Single-file `web/index.html`: Tailwind CSS + Alpine.js from a CDN, nothing else

Rejected on purpose: WhisperX (pins `pyannote-audio<4`), NeMo Sortformer (four speakers
max), a vector database (numpy cosine over the jobs table is enough), a task broker
(workers claim jobs straight from the database).

## Layout

```
app/config.py     settings; blank env values mean "not set"
app/models.py     tables, shared shapes, model-cache helpers
app/media.py      decoding any container, and the silence-trimming VAD pass
app/engines.py    the three models, loaded once
app/pipeline.py   whisper + pyannote -> named, timed, measured turns
app/voices.py     voice signatures: enrolment, recognition, live grouping
app/live.py       utterance-by-utterance transcription of a stream
app/insights.py   the Agno agents behind /summary and /ask
app/search.py     the passage index
app/worker.py     the database-backed job queue
app/api/          HTTP and WebSocket endpoints
web/index.html    the entire UI
```

## Commands

```bash
uv sync                          # UV_TORCH_BACKEND=auto picks the CPU/CUDA wheels
uv run fastapi run app/main.py
uv run fastapi run app/main.py --port 8009   # when 8000 is taken by something else
uv run pytest                    # models are stubbed; no weights downloaded
uv run ruff check . && uv run ruff format .
docker compose up --build
```

## Still open

From the two backlogs the owner reviewed. Everything else on those lists is built.

Highest value first: authentication and rate limiting (the API is wide open); export to
SRT/VTT/DOCX; a retention policy for transcripts; per-request
translation and language; word-level timestamps in the payload; webhooks or SSE instead
of polling; multi-channel recordings (one track per participant); chunked parallel
processing for long files; denoising for bad rooms; metrics and a WER/DER eval harness;
meeting series with carry-over of unfinished action items; pushing action items to
Jira/Linear and summaries to Slack; PII redaction; encryption at rest and an access log;
multi-tenancy.
