"""Live transcription.

The browser sends 16 kHz mono float32 PCM over a WebSocket. Silero VAD marks
where someone stops talking and every finished utterance is transcribed on its
own. The stream is recorded to disk and kept as an ordinary job, so a live
meeting ends up with the same playback, summary, search and analytics as an
uploaded one.
"""

import logging
import time
import wave
from collections.abc import Sequence

import numpy as np
from faster_whisper.vad import VadOptions, get_speech_timestamps
from sqlmodel import Session
from starlette.concurrency import run_in_threadpool

from app import pipeline, search
from app.media import SAMPLE_RATE
from app.models import Job, Person, Status, engine, now
from app.voices import Roll

VAD = VadOptions(min_silence_duration_ms=600, speech_pad_ms=200)
SILENCE_TAIL = int(0.6 * SAMPLE_RATE)  # quiet needed before an utterance counts as over
MIN_UTTERANCE = int(0.4 * SAMPLE_RATE)
MAX_BUFFER = 25 * SAMPLE_RATE  # cut a monologue rather than let latency grow
HEARTBEAT_SECONDS = 30  # so a silent stretch is not mistaken for a dead worker
log = logging.getLogger(__name__)


class LiveSession:
    """One microphone stream, recorded and transcribed into a job."""

    def __init__(self, job: Job, vocabulary: str | None = None, people: Sequence[Person] = ()):
        self.job_id = job.id
        self.vocabulary = vocabulary
        self.roll = Roll(people) if people else None
        self.turns: list[dict] = []
        self.buffer = np.zeros(0, dtype=np.float32)
        self.offset = 0.0  # seconds of audio already consumed
        self.reported = time.monotonic()
        self.recording = wave.open(str(job.media()), "wb")  # noqa: SIM115 - open for the session
        self.recording.setnchannels(1)
        self.recording.setsampwidth(2)
        self.recording.setframerate(SAMPLE_RATE)

    def add(self, pcm: bytes) -> None:
        samples = np.frombuffer(pcm, dtype=np.float32)
        self.recording.writeframes((np.clip(samples, -1, 1) * 32767).astype("<i2").tobytes())
        self.buffer = np.concatenate([self.buffer, samples])

    async def drain(self) -> list[dict]:
        """Transcribe every utterance the speaker has finished saying."""
        fresh = []
        while len(self.buffer) > MIN_UTTERANCE:
            speech = get_speech_timestamps(self.buffer, VAD)
            if not speech:
                self._advance(max(len(self.buffer) - SILENCE_TAIL, 0))  # nothing but silence
                break
            start, end = speech[0]["start"], speech[0]["end"]
            if len(self.buffer) - end < SILENCE_TAIL and len(self.buffer) < MAX_BUFFER:
                break  # still talking
            turn = await self._transcribe(self.buffer[start:end], start)
            self._advance(end)
            if turn:
                fresh.append(turn)
        if fresh:
            self._save(fresh)
        elif time.monotonic() - self.reported > HEARTBEAT_SECONDS:
            self._save([])  # a quiet meeting still has a live worker behind it
        return fresh

    async def finish(self, error: str | None = None) -> Job:
        """Transcribe the last sentence and close the job."""
        speech = get_speech_timestamps(self.buffer, VAD)
        if speech and speech[-1]["end"] - speech[0]["start"] >= MIN_UTTERANCE:
            start, end = speech[0]["start"], speech[-1]["end"]
            if turn := await self._transcribe(self.buffer[start:end], start):
                self.turns.append(turn)
        duration = self.offset + len(self.buffer) / SAMPLE_RATE
        self.recording.close()

        with Session(engine) as session:
            job = session.get(Job, self.job_id)
            job.result = self._result(duration)
            job.error = error
            job.status = Status.failed if error and not self.turns else Status.completed
            job.progress, job.finished_at = 1.0, now()
            job.metrics = {"media_seconds": round(duration, 1)}
            session.add(job)
            session.commit()
            search.index(session, job)
            session.refresh(job)
            return job

    def _result(self, duration: float) -> dict:
        speakers = sorted({turn["speaker"] for turn in self.turns if turn["speaker"]})
        result = {
            "duration": round(duration, 3),
            "speech_duration": round(sum(t["end"] - t["start"] for t in self.turns), 3),
            "language": None,
            "speakers": speakers,
            "segments": self.turns,
        }
        if speakers:  # without speaker labels there is no talk time to report
            result["analytics"] = pipeline.analytics_of(self.turns, duration)
        return result

    def _save(self, fresh: list[dict]) -> None:
        self.turns.extend(fresh)
        self.reported = time.monotonic()
        with Session(engine) as session:
            job = session.get(Job, self.job_id)
            job.result = self._result(self.offset + len(self.buffer) / SAMPLE_RATE)
            job.heartbeat_at = now()
            session.add(job)
            session.commit()

    def _advance(self, samples: int) -> None:
        self.offset += samples / SAMPLE_RATE
        self.buffer = self.buffer[samples:]

    async def _transcribe(self, audio: np.ndarray, at: int) -> dict | None:
        start = self.offset + at / SAMPLE_RATE
        segments, _ = await run_in_threadpool(pipeline.transcribe, audio, self.vocabulary)
        text = " ".join(segment.text.strip() for segment in segments).strip()
        if not text:
            return None
        speaker = None
        if self.roll:
            speaker = await run_in_threadpool(self.roll.label, audio)
        return {
            "start": round(start, 2),
            "end": round(start + len(audio) / SAMPLE_RATE, 2),
            "speaker": speaker,
            "text": text,
        }
