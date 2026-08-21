import uuid
from pathlib import Path
from typing import Annotated

from fastapi import Depends, HTTPException, Query, UploadFile
from sqlmodel import Session

from app.config import settings
from app.models import Job, Options, engine


def get_session():
    with Session(engine) as session:
        yield session


SessionDep = Annotated[Session, Depends(get_session)]
OptionsDep = Annotated[Options, Query()]


def job_or_404(job_id: uuid.UUID, session: Session) -> Job:
    if job := session.get(Job, job_id):
        return job
    raise HTTPException(404, "Job not found")


async def save_upload(file: UploadFile, destination: Path) -> None:
    """Stream an upload to disk, refusing anything oversized or empty."""
    limit, size = settings.max_upload_mb * 1024 * 1024, 0
    with destination.open("wb") as out:
        while chunk := await file.read(1 << 20):
            size += len(chunk)
            if size > limit:
                destination.unlink(missing_ok=True)
                raise HTTPException(413, f"Upload exceeds the {settings.max_upload_mb} MB limit")
            out.write(chunk)
    if not size:
        destination.unlink(missing_ok=True)
        raise HTTPException(400, "Empty upload")
