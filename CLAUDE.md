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
- **Nothing happens silently.** Startup logs the database, media directory, model cache
  and its size, role and device. Model loading logs what is being fetched and whether it
  came from the cache or was downloaded, with the size and elapsed time.
- **Models are downloaded once.** `MT_MODEL_CACHE` (or `HF_HOME`) must point at
  something persistent; the Docker image keeps it on a named volume.
- **Everything is kept and browsable.** Transcripts, summaries, analytics and notes live
  in the database indefinitely and are reachable from the Library tab. Media files live
  on the filesystem for `MT_KEEP_MEDIA_DAYS`.
- **Silence never reaches the models.** One VAD pass trims it, and timestamps are mapped
  back onto the original recording.
- **Audio and video are stored on the filesystem**, not in the database or an object
  store. Any container FFmpeg reads is accepted, video included.

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
uv run pytest                    # models are stubbed; no weights downloaded
uv run ruff check . && uv run ruff format .
docker compose up --build
```

## Still open

From the two backlogs the owner reviewed. Everything else on those lists is built.

Highest value first: authentication and rate limiting (the API is wide open); export to
SRT/VTT/DOCX; deleting a job and a retention policy for transcripts; per-request
translation and language; word-level timestamps in the payload; webhooks or SSE instead
of polling; multi-channel recordings (one track per participant); chunked parallel
processing for long files; denoising for bad rooms; metrics and a WER/DER eval harness;
meeting series with carry-over of unfinished action items; pushing action items to
Jira/Linear and summaries to Slack; PII redaction; encryption at rest and an access log;
multi-tenancy.
