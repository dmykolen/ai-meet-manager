import os
import uuid
from datetime import UTC, datetime, timedelta
from enum import StrEnum
from pathlib import Path
from secrets import token_urlsafe

from pydantic import BaseModel
from pydantic import Field as PField
from sqlalchemy import JSON, Column
from sqlalchemy.engine import make_url
from sqlmodel import Field, SQLModel, create_engine

from app.config import settings


def now() -> datetime:
    """Naive UTC, which is what the database stores."""
    return datetime.now(UTC).replace(tzinfo=None)


class Task(StrEnum):
    transcribe = "transcribe"
    diarize = "diarize"
    transcribe_diarize = "transcribe_diarize"


class Status(StrEnum):
    queued = "queued"
    running = "running"
    completed = "completed"
    failed = "failed"


class Options(BaseModel):
    """Per-request tuning. Speaker counts are ignored by /v1/transcribe."""

    vocabulary: str | None = PField(default=None, description="Names and jargon to expect")
    num_speakers: int | None = PField(default=None, ge=1, description="Exact speaker count")
    min_speakers: int | None = PField(default=None, ge=1)
    max_speakers: int | None = PField(default=None, ge=1)

    def speakers(self) -> dict[str, int]:
        counts = {"num_speakers", "min_speakers", "max_speakers"}
        return self.model_dump(exclude_none=True, include=counts)


class Job(SQLModel, table=True):
    id: uuid.UUID = Field(default_factory=uuid.uuid4, primary_key=True)
    task: Task
    status: Status = Field(default=Status.queued, index=True)
    filename: str
    content_type: str = "application/octet-stream"
    suffix: str = ""  # kept so the stored file stays playable and recognisable
    has_video: bool = False
    live: bool = False
    progress: float = 0.0
    attempts: int = 0
    media_kept: bool = True
    created_at: datetime = Field(default_factory=now, index=True)
    heartbeat_at: datetime | None = None  # a stale one means the worker died
    finished_at: datetime | None = None
    error: str | None = None
    options: dict = Field(default_factory=dict, sa_column=Column(JSON))
    # {"language", "duration", "speakers", "analytics", "segments": [...]}
    result: dict | None = Field(default=None, sa_column=Column(JSON))
    summary: dict | None = Field(default=None, sa_column=Column(JSON))
    metrics: dict = Field(default_factory=dict, sa_column=Column(JSON))
    # One voice signature per diarized speaker, so corrections can teach the roster.
    voiceprints: dict = Field(default_factory=dict, sa_column=Column(JSON))

    def media(self) -> Path:
        return settings.media_dir / f"{self.id}{self.suffix}"


class Person(SQLModel, table=True):
    """A known voice. Every recognised appearance adds another sample."""

    id: uuid.UUID = Field(default_factory=uuid.uuid4, primary_key=True)
    name: str = Field(index=True, unique=True)
    created_at: datetime = Field(default_factory=now)
    samples: list[list[float]] = Field(default_factory=list, sa_column=Column(JSON))


class Chunk(SQLModel, table=True):
    """A passage of a transcript, indexed so past meetings can be searched."""

    id: uuid.UUID = Field(default_factory=uuid.uuid4, primary_key=True)
    job_id: uuid.UUID = Field(foreign_key="job.id", index=True)
    start: float
    end: float
    speakers: str = ""
    text: str
    embedding: list[float] = Field(default_factory=list, sa_column=Column(JSON))


class Comment(SQLModel, table=True):
    id: uuid.UUID = Field(default_factory=uuid.uuid4, primary_key=True)
    job_id: uuid.UUID = Field(foreign_key="job.id", index=True)
    at: float = 0.0  # position in the recording the comment refers to
    author: str = "anonymous"
    text: str
    created_at: datetime = Field(default_factory=now)


class Share(SQLModel, table=True):
    """A read-only link to one transcript."""

    token: str = Field(default_factory=lambda: token_urlsafe(16), primary_key=True)
    job_id: uuid.UUID = Field(foreign_key="job.id", index=True)
    created_at: datetime = Field(default_factory=now)
    expires_at: datetime = Field(default_factory=lambda: now() + timedelta(settings.share_days))


url = make_url(settings.database_url)
engine = create_engine(
    settings.database_url,
    pool_pre_ping=True,
    connect_args={"check_same_thread": False} if url.get_backend_name() == "sqlite" else {},
)


def init_db() -> None:
    """Create the media directory, the SQLite folder and the tables."""
    if url.get_backend_name() == "sqlite" and url.database:
        Path(url.database).parent.mkdir(parents=True, exist_ok=True)
    settings.media_dir.mkdir(parents=True, exist_ok=True)
    SQLModel.metadata.create_all(engine)


def model_cache() -> Path:
    """Where Hugging Face keeps the downloaded weights between runs."""
    return Path(os.environ.get("HF_HOME") or Path.home() / ".cache" / "huggingface").resolve()


def cached_gigabytes() -> float:
    """How much is already downloaded, so a re-download is obvious in the log."""
    cache = model_cache()
    if not cache.exists():
        return 0.0
    return round(sum(f.stat().st_size for f in cache.rglob("*") if f.is_file()) / 1e9, 2)
