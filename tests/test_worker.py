"""The database-backed queue: claiming, retrying and taking over dead workers."""

from datetime import timedelta

import pytest
from sqlmodel import Session, select

from app import worker
from app.config import settings
from app.models import Job, Status, Task, engine, init_db, now


@pytest.fixture(autouse=True, scope="module")
def paused_workers():
    """The app's own workers would race these tests for the same jobs."""
    worker.stop()
    yield
    worker.start()


@pytest.fixture(autouse=True)
def database():
    init_db()
    yield
    with Session(engine) as session:
        for job in session.exec(select(Job).where(Job.filename.startswith("queue"))):
            session.delete(job)
        session.commit()


def queued(**fields) -> Job:
    with Session(engine) as session:
        job = Job(task=Task.transcribe, filename="queue.wav", **fields)
        session.add(job)
        session.commit()
        session.refresh(job)
        return job


def reload(job_id) -> Job:
    with Session(engine) as session:
        return session.get(Job, job_id)


def test_a_queued_job_is_claimed_once_and_only_once():
    job = queued()
    assert worker.claim() == job.id
    assert worker.claim() != job.id, "a second worker must not take the same job"
    taken = reload(job.id)
    assert taken.status is Status.running
    assert taken.attempts == 1
    assert taken.heartbeat_at is not None


def test_a_job_whose_worker_died_is_taken_over():
    job = queued(status=Status.running, heartbeat_at=now() - timedelta(seconds=10))
    assert worker.claim() != job.id, "a live worker's job is left alone"

    with Session(engine) as session:
        stale = session.get(Job, job.id)
        stale.heartbeat_at = now() - timedelta(seconds=settings.lease_seconds + 60)
        session.add(stale)
        session.commit()
    assert worker.claim() == job.id


def test_a_job_that_used_up_its_attempts_is_failed_rather_than_run(monkeypatch):
    job = queued(attempts=settings.max_attempts, error="disk on fire")
    assert worker.claim() == job.id
    worker.run(job.id)

    given_up = reload(job.id)
    assert given_up.status is Status.failed
    assert given_up.error == "disk on fire"
    assert given_up.finished_at is not None


def test_a_transient_failure_puts_the_job_back_in_the_queue(monkeypatch):
    from app import pipeline

    def broken(*_args, **_kwargs):
        raise RuntimeError("model server hiccup")

    monkeypatch.setattr(pipeline, "run", broken)
    job = queued()
    worker.claim()
    worker.run(job.id)

    assert reload(job.id).status is Status.queued, "it will be retried"
    worker.claim()
    worker.run(job.id)
    worker.claim()
    worker.run(job.id)
    assert reload(job.id).status is Status.failed, "but not forever"


def test_unplayable_media_is_never_retried(monkeypatch):
    from app import pipeline
    from app.media import UnplayableMedia

    def unplayable(*_args, **_kwargs):
        raise UnplayableMedia("no audio track")

    monkeypatch.setattr(pipeline, "run", unplayable)
    job = queued()
    worker.claim()
    worker.run(job.id)

    failed = reload(job.id)
    assert failed.status is Status.failed
    assert failed.attempts == 1, "retrying a file with no sound would never help"
