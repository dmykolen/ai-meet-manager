"""Job execution.

Workers claim jobs straight from the database, so the same code runs as one
process on a laptop and as several worker containers sharing a database and a
media volume. A claim bumps `attempts`, which is what makes it atomic: the
second worker's update matches no rows. A job whose worker dies stops sending
heartbeats and is taken over once its lease expires.
"""

import logging
import threading
import time
from datetime import timedelta
from uuid import UUID

from sqlalchemy import and_, or_, update
from sqlmodel import Session, col, select

from app import search, voices
from app.config import settings
from app.media import UnplayableMedia
from app.models import Job, Options, Status, Task, engine, now

log = logging.getLogger(__name__)

_wake = threading.Event()
_stop = threading.Event()
_threads: list[threading.Thread] = []


def start() -> None:
    """Run this process's share of the workers."""
    _stop.clear()
    for index in range(settings.concurrency):
        thread = threading.Thread(target=_loop, name=f"worker-{index}", daemon=True)
        thread.start()
        _threads.append(thread)
    log.info("Started %d worker(s)", settings.concurrency)


def stop() -> None:
    _stop.set()
    _wake.set()
    for thread in _threads:
        thread.join(timeout=2)
    _threads.clear()


def wake() -> None:
    """Skip the poll interval because a job was just queued in this process."""
    _wake.set()


def _loop() -> None:
    """One worker thread, which must outlive anything a single job can do to it.

    `run` swallows what a job raises, but everything around it — claiming, committing
    the final row — used to be able to kill the thread outright. With one worker that
    silently stops the whole queue, and the job it was on stays `running` for ever with
    nobody left to take it over.
    """
    while not _stop.is_set():
        try:
            while (job_id := claim()) is not None:
                run(job_id)
                if _stop.is_set():
                    return
        except Exception:
            log.exception("Worker loop stumbled; carrying on")
        _wake.wait(settings.poll_interval)
        _wake.clear()


def claim() -> UUID | None:
    """Take the oldest waiting job, or one whose worker stopped reporting in."""
    abandoned = now() - timedelta(seconds=settings.lease_seconds)
    waiting = (
        select(Job)
        .where(
            or_(
                col(Job.status) == Status.queued,
                and_(col(Job.status) == Status.running, col(Job.heartbeat_at) < abandoned),
            )
        )
        .order_by(col(Job.created_at))
        .limit(8)
    )
    with Session(engine) as session:
        for job in session.exec(waiting):
            taken = session.execute(
                update(Job)
                .where(
                    col(Job.id) == job.id,
                    col(Job.status) == job.status,
                    col(Job.attempts) == job.attempts,
                )
                .values(
                    status=Status.running,
                    attempts=job.attempts + 1,
                    heartbeat_at=now(),
                    progress=0.0,
                )
            )
            session.commit()
            if taken.rowcount == 1:
                return job.id
    return None


def run(job_id: UUID) -> None:
    """Process one claimed job to completion."""
    from app import pipeline  # deferred: the first job pays for loading the ML stack

    started = time.monotonic()
    with Session(engine) as session:
        job = session.get(Job, job_id)
        if job is None:
            return  # deleted in the moment between the claim and this fetch

        def report(fraction: float) -> None:  # monotonic, and doubles as the heartbeat
            fraction = round(min(max(fraction, job.progress), 1.0), 2)
            job.progress, job.heartbeat_at = fraction, now()
            session.commit()

        log.info("Job %s %s — %s", str(job.id)[:8], job.filename, job.task.replace("_", " + "))
        try:
            if job.attempts > settings.max_attempts:
                raise RuntimeError(job.error or "Given up after too many attempts")
            people = [] if job.task is Task.transcribe else voices.everyone(session)
            result, voiceprints = pipeline.run(job.media(), job.task, Options(**job.options), people, report)
            job.result, job.voiceprints = result, voiceprints
            job.metrics = _metrics(result, time.monotonic() - started)
            job.status, job.progress = Status.completed, 1.0
            for name, vector in voiceprints.items():
                voices.remember(session, name, vector)
            session.commit()
            search.index(session, job)
            log.info(
                "Job %s done in %.1fs (%sx realtime), %d rows, %d speakers",
                str(job.id)[:8],
                time.monotonic() - started,
                job.metrics.get("realtime_factor", "?"),
                len(result["segments"]),
                len(result["speakers"]),
            )
        except Exception as exc:
            log.exception("Job %s failed after %.1fs", str(job.id)[:8], time.monotonic() - started)
            fatal = isinstance(exc, UnplayableMedia) or job.attempts >= settings.max_attempts
            job.status = Status.failed if fatal else Status.queued
            job.error = str(exc)

        if job.status is not Status.queued:
            job.finished_at = now()
            if settings.keep_media_days <= 0:
                job.media().unlink(missing_ok=True)
                job.media_kept = False
        session.add(job)
        session.commit()
        again = job.status is Status.queued

    if again:
        wake()
    else:
        expire_media()


def expire_media() -> None:
    """Drop recordings that are past the retention window."""
    if settings.keep_media_days <= 0:
        return
    cutoff = now() - timedelta(days=settings.keep_media_days)
    with Session(engine) as session:
        stale = select(Job).where(col(Job.media_kept).is_(True), col(Job.created_at) < cutoff)
        for job in session.exec(stale):
            job.media().unlink(missing_ok=True)
            job.media_kept = False
            session.add(job)
        session.commit()


def _metrics(result: dict, seconds: float) -> dict:
    media_seconds = result.get("duration", 0.0)
    return {
        "media_seconds": round(media_seconds, 1),
        "speech_seconds": round(result.get("speech_duration", 0.0), 1),
        "processing_seconds": round(seconds, 1),
        "realtime_factor": round(media_seconds / seconds, 2) if seconds else 0.0,
    }
