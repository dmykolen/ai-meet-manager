import uuid
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Body, HTTPException, Query, UploadFile
from fastapi.responses import FileResponse
from sqlmodel import col, select
from starlette.concurrency import run_in_threadpool

from app import insights, media, search, voices, worker
from app.api.deps import OptionsDep, SessionDep, job_or_404, save_upload
from app.models import Comment, Job, Options, Share, Status, Task, now

router = APIRouter(prefix="/v1", tags=["jobs"])


async def accept(task: Task, file: UploadFile, session, options: Options) -> Job:
    """Store the upload and queue it. Any container FFmpeg reads is welcome."""
    job = Job(
        task=task,
        filename=file.filename or "recording",
        content_type=file.content_type or "application/octet-stream",
        suffix=Path(file.filename or "").suffix[:16],
        options=options.model_dump(exclude_none=True),
    )
    await save_upload(file, job.media())
    if not await run_in_threadpool(_playable, job):
        job.media().unlink(missing_ok=True)
        raise HTTPException(415, f"{job.filename} has no audio track we can decode")

    session.add(job)
    session.commit()
    session.refresh(job)
    worker.wake()
    return job


def _playable(job: Job) -> bool:
    try:
        media.decode(job.media())
    except media.UnplayableMedia:
        return False
    job.has_video = media.has_video(job.media())
    return True


@router.post("/transcribe", response_model=Job, status_code=202)
async def transcribe(file: UploadFile, session: SessionDep, options: OptionsDep) -> Job:
    """Speech to text with timestamps and per-segment confidence."""
    return await accept(Task.transcribe, file, session, options)


@router.post("/diarize", response_model=Job, status_code=202)
async def diarize(file: UploadFile, session: SessionDep, options: OptionsDep) -> Job:
    """Who spoke when, without transcribing."""
    return await accept(Task.diarize, file, session, options)


@router.post("/transcribe-diarize", response_model=Job, status_code=202)
async def transcribe_diarize(file: UploadFile, session: SessionDep, options: OptionsDep) -> Job:
    """Speech to text with a speaker on every turn."""
    return await accept(Task.transcribe_diarize, file, session, options)


@router.get("/jobs", response_model=list[Job])
def list_jobs(
    session: SessionDep,
    limit: Annotated[int, Query(ge=1, le=500)] = 50,
    q: str | None = None,
    status: Status | None = None,
) -> list[Job]:
    """Everything this instance has processed, newest first."""
    jobs = select(Job).order_by(col(Job.created_at).desc())
    if q:
        jobs = jobs.where(col(Job.filename).icontains(q))
    if status:
        jobs = jobs.where(col(Job.status) == status)
    return session.exec(jobs.limit(limit)).all()


@router.get("/jobs/{job_id}", response_model=Job)
def get_job(job_id: uuid.UUID, session: SessionDep) -> Job:
    return job_or_404(job_id, session)


@router.get("/jobs/{job_id}/media")
def get_media(job_id: uuid.UUID, session: SessionDep) -> FileResponse:
    """The original recording — audio or video — while it is still stored.

    Served inline and byte-range aware, so a player can seek into it.
    """
    job = job_or_404(job_id, session)
    if not job.media_kept or not job.media().exists():
        raise HTTPException(404, "The recording is no longer stored")
    return FileResponse(
        job.media(),
        media_type=job.content_type,
        filename=job.filename,
        content_disposition_type="inline",
    )


@router.post("/jobs/{job_id}/summary", response_model=Job)
async def summarise(job_id: uuid.UUID, session: SessionDep, refresh: bool = False) -> Job:
    """Overview, chapters, decisions and action items, written by the configured LLM."""
    job = job_or_404(job_id, session)
    if job.result is None or not insights.as_text(job.result):
        raise HTTPException(409, "This job has no transcript to summarise")
    if job.summary is None or refresh:
        try:
            job.summary, tokens = await run_in_threadpool(insights.summarise, job.result)
        except Exception as exc:
            raise HTTPException(502, f"Could not summarise: {exc}") from exc
        job.metrics = {**job.metrics, "summary_tokens": tokens}
        session.add(job)
        session.commit()
        session.refresh(job)
    return job


@router.patch("/jobs/{job_id}/speakers", response_model=Job)
def rename_speakers(
    job_id: uuid.UUID,
    session: SessionDep,
    names: Annotated[dict[str, str], Body(examples=[{"SPEAKER_00": "Priya"}])],
    remember: bool = True,
) -> Job:
    """Correct the speaker labels, and by default teach the roster those voices."""
    job = job_or_404(job_id, session)
    if job.result is None:
        raise HTTPException(409, "This job has no transcript yet")

    job.result = _relabel(job.result, names)
    job.voiceprints = {names.get(label, label): v for label, v in job.voiceprints.items()}
    session.add(job)
    session.commit()
    if remember:
        _enrol_corrections(session, job, set(names.values()))
    search.index(session, job)
    session.refresh(job)
    return job


def _relabel(result: dict, names: dict[str, str]) -> dict:
    renamed = {**result}
    renamed["segments"] = [
        {**turn, "speaker": names.get(turn["speaker"], turn["speaker"])}
        if turn.get("speaker")
        else turn
        for turn in result.get("segments", [])
    ]
    renamed["speakers"] = sorted({names.get(s, s) for s in result.get("speakers", [])})
    if analytics := result.get("analytics"):
        renamed["analytics"] = {
            **analytics,
            "speakers": [
                {**row, "speaker": names.get(row["speaker"], row["speaker"])}
                for row in analytics.get("speakers", [])
            ],
        }
    return renamed


def _enrol_corrections(session, job: Job, named: set[str]) -> None:
    """A corrected label is the cheapest voice sample there is."""
    for name in named:
        if (vector := job.voiceprints.get(name)) is not None:
            voices.remember(session, name, vector, enrol=True)
    session.commit()


# --- comments and sharing -----------------------------------------------------


@router.get("/jobs/{job_id}/comments", response_model=list[Comment])
def list_comments(job_id: uuid.UUID, session: SessionDep) -> list[Comment]:
    return session.exec(
        select(Comment).where(col(Comment.job_id) == job_id).order_by(col(Comment.at))
    ).all()


@router.post("/jobs/{job_id}/comments", response_model=Comment, status_code=201)
def add_comment(job_id: uuid.UUID, session: SessionDep, comment: Comment) -> Comment:
    job_or_404(job_id, session)
    comment = Comment(job_id=job_id, at=comment.at, author=comment.author, text=comment.text)
    session.add(comment)
    session.commit()
    session.refresh(comment)
    return comment


@router.post("/jobs/{job_id}/share", response_model=Share, status_code=201)
def share(job_id: uuid.UUID, session: SessionDep) -> Share:
    """A read-only link to this transcript, good until it expires."""
    job_or_404(job_id, session)
    link = Share(job_id=job_id)
    session.add(link)
    session.commit()
    session.refresh(link)
    return link


@router.get("/shared/{token}", response_model=Job)
def open_shared(token: str, session: SessionDep) -> Job:
    link = session.get(Share, token)
    if link is None or link.expires_at < now():
        raise HTTPException(404, "This link has expired")
    job = session.get(Job, link.job_id)
    if job is None or job.status is not Status.completed:
        raise HTTPException(404, "Nothing to show yet")
    return job
