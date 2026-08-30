import uuid
from typing import Annotated

from fastapi import APIRouter, Form, HTTPException, UploadFile
from sqlmodel import col, select
from starlette.concurrency import run_in_threadpool

from app import media, voices
from app.api.deps import SessionDep, save_upload
from app.config import settings
from app.models import Person

router = APIRouter(prefix="/v1/people", tags=["voices"])


@router.post("", response_model=Person, status_code=201)
async def enrol(session: SessionDep, file: UploadFile, name: Annotated[str, Form()]) -> Person:
    """Learn a voice from a sample of one person speaking alone.

    Refused if a second voice is in it or if there is less speech than
    `MT_ENROL_MIN_SECONDS`: a bad sample here mislabels every meeting after it.
    """
    if session.exec(select(Person).where(Person.name == name)).first():
        raise HTTPException(409, f"{name} is already enrolled")

    sample = settings.media_dir / f"enrol-{uuid.uuid4()}"
    try:
        await save_upload(file, sample)
        vector = await run_in_threadpool(lambda: voices.signature(media.decode(sample)))
    except media.UnplayableMedia as exc:
        raise HTTPException(415, str(exc)) from exc
    except ValueError as exc:  # too little speech, or somebody else talking over it
        raise HTTPException(422, str(exc)) from exc
    finally:
        sample.unlink(missing_ok=True)

    person = Person(name=name, samples=[vector])
    session.add(person)
    session.commit()
    session.refresh(person)
    return person


@router.get("", response_model=list[Person])
def list_people(session: SessionDep) -> list[Person]:
    return session.exec(select(Person).order_by(col(Person.name))).all()


@router.delete("/{person_id}", status_code=204)
def forget(person_id: uuid.UUID, session: SessionDep) -> None:
    if person := session.get(Person, person_id):
        session.delete(person)
        session.commit()
