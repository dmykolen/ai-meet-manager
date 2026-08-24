import time
import uuid
from typing import ClassVar

import numpy as np
import pytest
from pyannote.audio.pipelines.speaker_diarization import DiarizeOutput
from pyannote.core import Annotation, Segment
from sqlmodel import Session, select
from test_pipeline import DIMA, OLENA, Chunk, Word

from app import engines, insights, pipeline, search, voices, worker
from app.config import settings
from app.media import SAMPLE_RATE
from app.models import Job, Person, engine


class FakeASR:
    """Stands in for WhisperModel so the tests need no model weights."""

    calls: ClassVar[list[dict]] = []

    def transcribe(self, audio, word_timestamps=False, **kwargs):
        assert audio.ndim == 1, "pyannote and whisper share one mono waveform"
        language = kwargs.get("language")
        if language is not None and language not in {"en", "uk", "de"}:
            raise ValueError(f"{language!r} is not a valid language code")  # as Whisper does
        if "batch_size" in kwargs and not kwargs.get("vad_filter"):
            raise RuntimeError(  # as BatchedInferencePipeline does past 30 seconds
                "No clip timestamps found. Set 'vad_filter' to True or provide 'clip_timestamps'."
            )
        FakeASR.calls.append({"word_timestamps": word_timestamps, **kwargs})
        chunks = [
            Chunk(Word(" Hello", 0.0, 0.5), Word(" everyone", 0.5, 1.0)),
            Chunk(Word(" Hi", 2.1, 2.6)),
        ]
        for chunk in chunks:
            if not word_timestamps:
                chunk.words = None
        return iter(chunks), type("Info", (), {"language": "en"})


def fake_diarizer(audio, hook=None, **kwargs):
    assert set(audio) == {"waveform", "sample_rate"}, "pyannote is fed an in-memory waveform"
    assert audio["waveform"].shape[0] == 1, "pyannote expects (channel, time)"
    assert audio["sample_rate"] == SAMPLE_RATE
    assert set(kwargs) <= {"num_speakers", "min_speakers", "max_speakers"}
    if hook:
        hook("segmentation", None, total=2, completed=1)
        hook("clustering", None, total=2, completed=2)
    speech = Annotation()
    speech[Segment(0, 2)] = "SPEAKER_00"
    speech[Segment(2, 4)] = "SPEAKER_01"
    return DiarizeOutput(
        speaker_diarization=speech,
        exclusive_speaker_diarization=speech,
        speaker_embeddings=np.array([DIMA, OLENA], dtype=np.float32),
    )


@pytest.fixture(autouse=True)
def stub_models(monkeypatch):
    FakeASR.calls.clear()
    monkeypatch.setattr(pipeline, "asr", FakeASR)
    monkeypatch.setattr(pipeline, "diarizer", lambda: fake_diarizer)
    monkeypatch.setattr(engines, "warm_up", lambda: None)


@pytest.fixture(autouse=True)
def clean_roster():
    yield
    with Session(engine) as session:
        for person in session.exec(select(Person)):
            session.delete(person)
        session.commit()


def submit(client, endpoint, media, name="meeting.wav", **params):
    response = client.post(endpoint, files={"file": (name, media)}, params=params)
    assert response.status_code == 202, response.text
    return wait(client, response.json()["id"])


def wait(client, job_id):
    for _ in range(200):
        job = client.get(f"/v1/jobs/{job_id}").json()
        if job["status"] in ("completed", "failed"):
            return job
        time.sleep(0.05)
    raise AssertionError("job did not finish")


# --- transcription ------------------------------------------------------------


def test_transcribe_returns_timestamped_text(client, wav_bytes):
    job = submit(client, "/v1/transcribe", wav_bytes)
    assert job["status"] == "completed", job["error"]
    assert job["result"]["language"] == "en"
    assert job["result"]["segments"][0] == {
        "start": 0.0,
        "end": 1.0,
        "text": "Hello everyone",
        "confidence": 0.819,
    }
    assert job["progress"] == 1.0
    assert job["metrics"]["realtime_factor"] > 0


def test_diarize_reports_turns_and_talk_time(client, wav_bytes):
    job = submit(client, "/v1/diarize", wav_bytes, num_speakers=2)
    assert job["result"]["segments"][0] == {"start": 0.0, "end": 2.0, "speaker": "SPEAKER_00"}
    assert job["result"]["analytics"]["speakers"][0]["seconds"] == 2.0
    assert "text" not in job["result"]["segments"][0]


def test_transcribe_diarize_labels_every_turn(client, wav_bytes):
    job = submit(client, "/v1/transcribe-diarize", wav_bytes, min_speakers=1, max_speakers=4)
    assert [turn["speaker"] for turn in job["result"]["segments"]] == ["SPEAKER_00", "SPEAKER_01"]
    assert job["result"]["analytics"]["overlap_seconds"] == 0.0
    assert job["voiceprints"].keys() == {"SPEAKER_00", "SPEAKER_01"}


# --- video --------------------------------------------------------------------


def test_video_is_accepted_and_its_audio_transcribed(client, video_bytes):
    job = submit(client, "/v1/transcribe", video_bytes, name="standup.mp4")
    assert job["status"] == "completed", job["error"]
    assert job["has_video"] is True
    assert job["suffix"] == ".mp4"
    assert client.get(f"/v1/jobs/{job['id']}/media").content == video_bytes


def test_a_video_without_sound_is_refused_on_upload(client, silent_video_bytes):
    response = client.post("/v1/transcribe", files={"file": ("mute.mp4", silent_video_bytes)})
    assert response.status_code == 415


def test_a_file_that_is_not_media_is_refused_on_upload(client):
    response = client.post("/v1/transcribe", files={"file": ("notes.txt", b"not a recording")})
    assert response.status_code == 415


@pytest.mark.parametrize("suffix", [".mp3", ".m4a", ".ogg", ".flac", ".webm", ".mkv"])
def test_accepts_common_recording_formats(client, tmp_path, wav_bytes, suffix):
    from tests.conftest import encode

    source = tmp_path / "in.wav"
    source.write_bytes(wav_bytes)
    target = encode(f"converted{suffix}", "-i", str(source))
    job = submit(client, "/v1/transcribe", target.read_bytes(), name=target.name)
    assert job["status"] == "completed", job["error"]


# --- vocabulary ---------------------------------------------------------------


def test_vocabulary_is_passed_to_the_recogniser(client, wav_bytes):
    submit(client, "/v1/transcribe", wav_bytes, vocabulary="Kubernetes, Priya")
    assert FakeASR.calls[-1]["hotwords"] == "Kubernetes, Priya"


def test_the_configured_vocabulary_is_the_default(client, wav_bytes, monkeypatch):
    monkeypatch.setattr(settings, "vocabulary", "Anthropic")
    submit(client, "/v1/transcribe", wav_bytes)
    assert FakeASR.calls[-1]["hotwords"] == "Anthropic"


def test_whisper_skips_its_own_vad_because_silence_is_already_gone(client, wav_bytes, monkeypatch):
    monkeypatch.setattr(settings, "batch_size", 1)
    submit(client, "/v1/transcribe", wav_bytes)
    assert FakeASR.calls[-1]["vad_filter"] is False
    assert "batch_size" not in FakeASR.calls[-1]


def test_live_decodes_greedily_because_one_utterance_stands_alone(monkeypatch):
    """Beam search doubles the cost of every phrase and, measured, buys nothing here."""
    monkeypatch.setattr(pipeline, "live_asr", FakeASR)
    pipeline.transcribe(np.zeros(SAMPLE_RATE, dtype=np.float32), live=True, language="en")
    call = FakeASR.calls[-1]
    assert call["beam_size"] == 1 and call["condition_on_previous_text"] is False
    assert "batch_size" not in call, "batching is a wash on one short utterance"


def test_batched_decoding_keeps_the_vad_the_batching_itself_needs(client, wav_bytes):
    """Its VAD is the chunker, not a silence trimmer, and it refuses to run without one."""
    submit(client, "/v1/transcribe", wav_bytes)
    assert FakeASR.calls[-1]["batch_size"] == settings.batch_size
    assert FakeASR.calls[-1]["vad_filter"] is True


# --- known voices -------------------------------------------------------------


def enrol(client, name, media, monkeypatch, vector=DIMA):
    monkeypatch.setattr(voices, "embed", lambda _audio: vector)
    return client.post("/v1/people", data={"name": name}, files={"file": ("s.wav", media)})


def test_enrolled_voices_replace_anonymous_speaker_labels(client, wav_bytes, monkeypatch):
    assert enrol(client, "Dima", wav_bytes, monkeypatch).status_code == 201
    job = submit(client, "/v1/transcribe-diarize", wav_bytes)
    assert job["result"]["speakers"] == ["Dima", "SPEAKER_01"]


def test_every_recognised_appearance_adds_another_sample(client, wav_bytes, monkeypatch):
    enrol(client, "Dima", wav_bytes, monkeypatch)
    submit(client, "/v1/transcribe-diarize", wav_bytes)
    submit(client, "/v1/transcribe-diarize", wav_bytes)
    with Session(engine) as session:
        dima = session.exec(select(Person).where(Person.name == "Dima")).one()
        assert len(dima.samples) == 3  # the enrolment plus two meetings


def test_samples_per_person_are_capped(client, wav_bytes, monkeypatch):
    enrol(client, "Dima", wav_bytes, monkeypatch)
    monkeypatch.setattr(voices, "SAMPLES_PER_PERSON", 2)
    for _ in range(3):
        submit(client, "/v1/transcribe-diarize", wav_bytes)
    with Session(engine) as session:
        assert len(session.exec(select(Person)).one().samples) == 2


def test_a_name_can_only_be_enrolled_once(client, wav_bytes, monkeypatch):
    assert enrol(client, "Olena", wav_bytes, monkeypatch).status_code == 201
    assert enrol(client, "Olena", wav_bytes, monkeypatch).status_code == 409


def test_a_sample_without_speech_is_rejected(client, wav_bytes, monkeypatch):
    assert enrol(client, "Nobody", wav_bytes, monkeypatch, vector=None).status_code == 422


# --- corrections --------------------------------------------------------------


def test_renaming_a_speaker_rewrites_the_transcript_and_learns_the_voice(client, wav_bytes):
    job = submit(client, "/v1/transcribe-diarize", wav_bytes)
    fixed = client.patch(f"/v1/jobs/{job['id']}/speakers", json={"SPEAKER_00": "Priya"}).json()

    assert fixed["result"]["speakers"] == ["Priya", "SPEAKER_01"]
    assert fixed["result"]["segments"][0]["speaker"] == "Priya"
    assert fixed["result"]["analytics"]["speakers"][0]["speaker"] == "Priya"
    with Session(engine) as session:
        priya = session.exec(select(Person).where(Person.name == "Priya")).one()
        assert priya.samples == [DIMA]  # the voice that was labelled SPEAKER_00


def test_a_correction_can_be_kept_out_of_the_roster(client, wav_bytes):
    job = submit(client, "/v1/transcribe-diarize", wav_bytes)
    client.patch(
        f"/v1/jobs/{job['id']}/speakers",
        json={"SPEAKER_00": "Guest"},
        params={"remember": False},
    )
    assert client.get("/v1/people").json() == []


# --- recordings ---------------------------------------------------------------


def test_recordings_are_dropped_immediately_when_retention_is_off(client, wav_bytes, monkeypatch):
    monkeypatch.setattr(settings, "keep_media_days", 0)
    job = submit(client, "/v1/transcribe", wav_bytes)
    assert client.get(f"/v1/jobs/{job['id']}/media").status_code == 404


def test_expired_recordings_are_cleaned_up(client, wav_bytes):
    from datetime import timedelta

    from app.models import now

    job = submit(client, "/v1/transcribe", wav_bytes)
    with Session(engine) as session:
        stored = session.get(Job, uuid.UUID(job["id"]))
        stored.created_at = now() - timedelta(days=settings.keep_media_days + 1)
        session.add(stored)
        session.commit()
    worker.expire_media()
    assert client.get(f"/v1/jobs/{job['id']}/media").status_code == 404


# --- sharing and comments -----------------------------------------------------


def test_a_share_link_exposes_the_transcript_read_only(client, wav_bytes):
    job = submit(client, "/v1/transcribe", wav_bytes)
    token = client.post(f"/v1/jobs/{job['id']}/share").json()["token"]
    shared = client.get(f"/v1/shared/{token}")
    assert shared.status_code == 200
    assert shared.json()["result"] == job["result"]


def test_an_expired_link_stops_working(client, wav_bytes):
    from datetime import timedelta

    from app.models import Share, now

    job = submit(client, "/v1/transcribe", wav_bytes)
    token = client.post(f"/v1/jobs/{job['id']}/share").json()["token"]
    with Session(engine) as session:
        link = session.get(Share, token)
        link.expires_at = now() - timedelta(days=1)
        session.add(link)
        session.commit()
    assert client.get(f"/v1/shared/{token}").status_code == 404


def test_unknown_links_are_not_found(client):
    assert client.get("/v1/shared/nope").status_code == 404


def test_comments_are_kept_against_their_moment_in_the_recording(client, wav_bytes):
    job = submit(client, "/v1/transcribe", wav_bytes)
    client.post(
        f"/v1/jobs/{job['id']}/comments",
        json={"at": 12.5, "author": "Dima", "text": "check this number", "job_id": job["id"]},
    )
    comments = client.get(f"/v1/jobs/{job['id']}/comments").json()
    assert [(c["at"], c["author"], c["text"]) for c in comments] == [
        (12.5, "Dima", "check this number")
    ]


# --- summaries, search and questions ------------------------------------------


def summary_of(_result):
    return {"overview": "A sync.", "chapters": [{"start": 0.0, "title": "Intro"}]}, 1234


def test_summary_includes_chapters_and_is_generated_once(client, wav_bytes, monkeypatch):
    runs = []
    monkeypatch.setattr(
        insights, "summarise", lambda result: (runs.append(result), summary_of(result))[1]
    )
    job = submit(client, "/v1/transcribe-diarize", wav_bytes)

    summarised = client.post(f"/v1/jobs/{job['id']}/summary").json()
    assert summarised["summary"]["chapters"][0]["title"] == "Intro"
    assert summarised["metrics"]["summary_tokens"] == 1234
    client.post(f"/v1/jobs/{job['id']}/summary")
    assert len(runs) == 1
    client.post(f"/v1/jobs/{job['id']}/summary", params={"refresh": True})
    assert len(runs) == 2


def test_llm_failures_surface_as_a_gateway_error(client, wav_bytes, monkeypatch):
    def broken(_result):
        raise RuntimeError("no api key")

    monkeypatch.setattr(insights, "summarise", broken)
    job = submit(client, "/v1/transcribe", wav_bytes)
    response = client.post(f"/v1/jobs/{job['id']}/summary")
    assert response.status_code == 502
    assert "no api key" in response.json()["detail"]


def test_finished_meetings_become_searchable(client, wav_bytes):
    submit(client, "/v1/transcribe", wav_bytes)
    hits = client.get("/v1/search", params={"q": "everyone"}).json()
    assert hits and "Hello everyone" in hits[0]["text"]


def test_search_ranks_by_meaning_when_embeddings_are_available(client, wav_bytes, monkeypatch):
    monkeypatch.setattr(search, "embed", lambda texts: [[1.0, 0.0] for _ in texts])
    submit(client, "/v1/transcribe", wav_bytes)  # indexed with vectors this time
    hits = client.get("/v1/search", params={"q": "words that appear nowhere"}).json()
    assert hits, "a semantic match needs no shared words"


def test_questions_are_answered_from_the_transcripts(client, wav_bytes, monkeypatch):
    monkeypatch.setattr(insights, "answer", lambda question, passages: f"{len(passages)} passages")
    submit(client, "/v1/transcribe", wav_bytes)
    reply = client.post("/v1/ask", json={"question": "who said hello"}).json()
    assert reply["answer"].endswith("passages")
    assert reply["sources"]


def test_a_question_nothing_covers_returns_404(client):
    assert client.post("/v1/ask", json={"question": "zzzzqqq"}).status_code == 404


# --- misc ---------------------------------------------------------------------


def test_empty_upload_is_rejected(client):
    assert client.post("/v1/transcribe", files={"file": ("x.wav", b"")}).status_code == 400


def test_oversized_upload_is_rejected(client, monkeypatch):
    monkeypatch.setattr(settings, "max_upload_mb", 0)
    assert client.post("/v1/transcribe", files={"file": ("x.wav", b"0123")}).status_code == 413


def test_unknown_job_returns_404(client):
    assert client.get(f"/v1/jobs/{uuid.uuid4()}").status_code == 404


def test_health_reports_the_active_device(client):
    body = client.get("/health").json()
    assert body["status"] == "ok"
    assert body["device"] in ("cpu", "cuda")


def test_readiness_depends_on_the_models_loading(client, monkeypatch):
    assert client.get("/health/ready").json()["status"] == "ready"
    monkeypatch.setattr(engines, "warm_up", lambda: (_ for _ in ()).throw(RuntimeError("no token")))
    assert client.get("/health/ready").status_code == 503


def test_recordings_are_served_for_playback_not_download(client, video_bytes):
    """A player needs an inline body it can seek into, not an attachment."""
    job = submit(client, "/v1/transcribe", video_bytes, name="standup.mp4")
    response = client.get(f"/v1/jobs/{job['id']}/media")
    assert response.headers["content-disposition"].startswith("inline")
    assert response.headers["accept-ranges"] == "bytes"

    partial = client.get(f"/v1/jobs/{job['id']}/media", headers={"Range": "bytes=0-1023"})
    assert partial.status_code == 206
    assert len(partial.content) == 1024


def test_a_diarization_has_nothing_to_summarise(client, wav_bytes):
    job = submit(client, "/v1/diarize", wav_bytes)
    response = client.post(f"/v1/jobs/{job['id']}/summary")
    assert response.status_code == 409
    assert "no transcript" in response.json()["detail"]


def test_history_can_be_searched_and_filtered(client, wav_bytes):
    submit(client, "/v1/transcribe", wav_bytes, name="board-review.wav")
    submit(client, "/v1/transcribe", wav_bytes, name="standup.wav")

    found = client.get("/v1/jobs", params={"q": "BOARD"}).json()
    assert [job["filename"] for job in found] == ["board-review.wav"], "the search ignores case"
    assert client.get("/v1/jobs", params={"status": "failed"}).json() == []
    assert len(client.get("/v1/jobs", params={"status": "completed"}).json()) >= 2


def test_a_blank_setting_means_unset_not_empty(monkeypatch):
    """A .env written from the example leaves MT_LANGUAGE= behind; Whisper rejects ''."""
    from app.config import Settings

    for name in ("MT_LANGUAGE", "MT_HF_TOKEN", "MT_LLM_API_KEY", "MT_MODEL_CACHE"):
        monkeypatch.setenv(name, "")
    blank = Settings(_env_file=None)
    assert (blank.language, blank.hf_token, blank.llm_api_key) == (None, None, None)
    assert blank.model_cache is None, "an empty path would point the model cache at the cwd"


def test_the_language_is_never_passed_as_an_empty_string(client, wav_bytes, monkeypatch):
    monkeypatch.setattr(settings, "language", None)
    submit(client, "/v1/transcribe", wav_bytes)
    assert FakeASR.calls[-1]["language"] is None
