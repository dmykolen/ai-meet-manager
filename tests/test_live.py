import numpy as np
import pytest
from sqlmodel import Session

from app import live, pipeline, voices
from app.live import MAX_BUFFER, LiveSession
from app.media import SAMPLE_RATE
from app.models import Job, Person, Status, Task, engine, init_db


def speech(seconds: float) -> np.ndarray:
    return np.full(int(seconds * SAMPLE_RATE), 0.8, dtype=np.float32)


def silence(seconds: float) -> np.ndarray:
    return np.zeros(int(seconds * SAMPLE_RATE), dtype=np.float32)


def fake_vad(audio, *_args, **_kwargs) -> list[dict]:
    """Anything loud counts as speech, so the tests control utterance boundaries."""
    loud = np.flatnonzero(np.abs(audio) > 0.5)
    if not len(loud):
        return []
    breaks = np.flatnonzero(np.diff(loud) > 1)
    starts = np.concatenate([[loud[0]], loud[breaks + 1]])
    ends = np.concatenate([loud[breaks], [loud[-1]]])
    return [{"start": int(s), "end": int(e) + 1} for s, e in zip(starts, ends, strict=True)]


class Said:
    def __init__(self, text: str):
        self.text = text


@pytest.fixture(autouse=True)
def stub(monkeypatch):
    monkeypatch.setattr(live, "get_speech_timestamps", fake_vad)
    monkeypatch.setattr(
        pipeline, "transcribe", lambda audio, vocabulary=None: ([Said(" hello")], "en")
    )


@pytest.fixture
def session_and_job():
    init_db()
    with Session(engine) as db:
        job = Job(task=Task.transcribe, status=Status.running, filename="live.wav", suffix=".wav")
        db.add(job)
        db.commit()
        db.refresh(job)
    return LiveSession(job), job


async def feed(session: LiveSession, *parts: np.ndarray) -> list[dict]:
    session.add(np.concatenate(parts).tobytes())
    return await session.drain()


@pytest.mark.anyio
async def test_an_utterance_is_held_back_until_the_speaker_pauses(session_and_job):
    stream, _ = session_and_job
    assert await feed(stream, speech(1.0), silence(0.3)) == []
    turns = await feed(stream, silence(0.5))
    assert len(turns) == 1
    assert turns[0]["start"] == 0.0
    assert turns[0]["end"] == pytest.approx(1.0, abs=0.01)
    assert turns[0]["text"] == "hello"


@pytest.mark.anyio
async def test_timestamps_continue_across_utterances(session_and_job):
    stream, _ = session_and_job
    await feed(stream, speech(1.0), silence(0.8))
    turns = await feed(stream, speech(1.0), silence(0.8))
    assert turns[0]["start"] == pytest.approx(1.8, abs=0.05)


@pytest.mark.anyio
async def test_a_long_monologue_is_cut_rather_than_buffered_forever(session_and_job):
    stream, _ = session_and_job
    turns = await feed(stream, speech(MAX_BUFFER / SAMPLE_RATE + 1))
    assert len(turns) == 1
    assert len(stream.buffer) < MAX_BUFFER


@pytest.mark.anyio
async def test_silence_does_not_grow_the_buffer(session_and_job):
    stream, _ = session_and_job
    assert await feed(stream, silence(10)) == []
    assert len(stream.buffer) <= int(0.6 * SAMPLE_RATE)


@pytest.mark.anyio
async def test_enrolled_voices_are_named_in_the_live_stream(session_and_job, monkeypatch):
    monkeypatch.setattr(voices.Roll, "label", lambda self, audio: "Dima")
    stream, _ = session_and_job
    stream.roll = voices.Roll([Person(name="Dima", samples=[[1.0]])])
    turns = await feed(stream, speech(1.0), silence(0.8))
    assert turns[0]["speaker"] == "Dima"
    finished = await stream.finish()
    assert finished.result["analytics"]["speakers"][0]["speaker"] == "Dima"


@pytest.mark.anyio
async def test_speakers_are_unnamed_when_nobody_is_enrolled(session_and_job):
    stream, _ = session_and_job
    turns = await feed(stream, speech(1.0), silence(0.8))
    assert turns[0]["speaker"] is None


@pytest.mark.anyio
async def test_the_session_is_recorded_and_kept_as_a_job(session_and_job):
    stream, job = session_and_job
    await feed(stream, speech(1.0), silence(0.8))
    await feed(stream, speech(1.0))  # still being spoken when the stream ends
    finished = await stream.finish()

    assert finished.status is Status.completed
    assert len(finished.result["segments"]) == 2, "the unfinished sentence is transcribed too"
    assert job.media().exists(), "the recording is on disk for playback"
    assert job.media().stat().st_size > SAMPLE_RATE * 2, "and holds the audio it heard"


@pytest.mark.anyio
async def test_turns_are_visible_while_the_meeting_is_still_running(session_and_job):
    stream, job = session_and_job
    await feed(stream, speech(1.0), silence(0.8))
    with Session(engine) as db:
        assert db.get(Job, job.id).result["segments"][0]["text"] == "hello"
    await stream.finish()


def test_the_websocket_streams_turns_and_closes_with_the_transcript(client, monkeypatch):
    monkeypatch.setattr(live, "get_speech_timestamps", fake_vad)
    monkeypatch.setattr(
        pipeline, "transcribe", lambda audio, vocabulary=None: ([Said(" hello")], "en")
    )
    with client.websocket_connect("/v1/stream") as socket:
        ready = socket.receive_json()
        assert ready["sample_rate"] == SAMPLE_RATE and ready["job_id"]
        socket.send_bytes(np.concatenate([speech(1.0), silence(0.8)]).tobytes())
        assert socket.receive_json()["text"] == "hello"
        socket.send_text("stop")
        done = socket.receive_json()

    assert done["type"] == "done"
    assert done["result"]["segments"][0]["text"] == "hello"
    assert client.get(f"/v1/jobs/{done['job_id']}").json()["live"] is True


@pytest.mark.anyio
async def test_a_live_session_that_fell_over_is_marked_failed(session_and_job):
    stream, _ = session_and_job
    finished = await stream.finish("'' is not a valid language code")
    assert finished.status is Status.failed
    assert "language code" in finished.error


@pytest.mark.anyio
async def test_a_hiccup_after_real_speech_still_keeps_the_transcript(session_and_job):
    stream, _ = session_and_job
    await feed(stream, speech(1.0), silence(0.8))
    finished = await stream.finish("connection reset")
    assert finished.status is Status.completed
    assert len(finished.result["segments"]) == 1


@pytest.mark.anyio
async def test_a_session_without_speaker_following_reports_no_talk_time(session_and_job):
    stream, _ = session_and_job
    await feed(stream, speech(1.0), silence(0.8))
    finished = await stream.finish()
    assert "analytics" not in finished.result, "no labels means no talk time to attribute"
    assert len(finished.result["segments"]) == 1
