"""Search across past meetings.

Transcripts are cut into passages and embedded once, when a job finishes. The
vectors live in the jobs database and are compared in memory, which keeps the
deployment to one process and is fast enough for the tens of thousands of
passages a team accumulates. Without an embedding provider the same passages are
still searchable by keyword.
"""

import logging
from functools import lru_cache

import numpy as np
from sqlmodel import Session, col, delete, select

from app.config import settings
from app.insights import clock
from app.models import Chunk, Job

PASSAGE_SECONDS = 45
log = logging.getLogger(__name__)


@lru_cache(maxsize=1)
def _client():
    from openai import OpenAI  # deferred: ~2.5s of imports

    return OpenAI(api_key=settings.llm_api_key) if settings.llm_api_key else OpenAI()


def embed(texts: list[str]) -> list[list[float]]:
    """Embeddings for a batch of passages, or nothing if none can be had."""
    try:
        response = _client().embeddings.create(model=settings.embedding_model, input=texts)
        return [item.embedding for item in response.data]
    except Exception as exc:
        log.warning("Falling back to keyword search: %s", exc)
        return []


def index(session: Session, job: Job) -> None:
    """(Re)build the searchable passages of one job."""
    session.exec(delete(Chunk).where(col(Chunk.job_id) == job.id))
    passages = _passages(job.result or {})
    if not passages:
        session.commit()
        return
    vectors = embed([passage["text"] for passage in passages])
    for passage, vector in zip(passages, vectors or [[]] * len(passages), strict=True):
        session.add(Chunk(job_id=job.id, embedding=list(vector), **passage))
    session.commit()


def find(session: Session, query: str, limit: int | None = None) -> list[tuple[Chunk, Job]]:
    """The passages that best answer a question, newest meetings breaking ties."""
    rows = session.exec(select(Chunk, Job).where(col(Chunk.job_id) == Job.id)).all()
    if not rows:
        return []
    vector = embed([query])
    if vector and any(chunk.embedding for chunk, _ in rows):
        known = [(chunk, job) for chunk, job in rows if chunk.embedding]
        matrix = np.array([chunk.embedding for chunk, _ in known])
        scores = _unit(matrix) @ _unit(np.array(vector))[0]
    else:
        words = {word for word in query.lower().split() if len(word) > 2}
        known = rows
        scores = np.array([_overlap(chunk.text, words) for chunk, _ in known])
    best = np.argsort(-scores)[: limit or settings.search_results]
    return [known[index] for index in best if scores[index] > 0]


def passage_label(chunk: Chunk, job: Job) -> str:
    return f"[{job.filename} @ {clock(chunk.start)}] {chunk.speakers}: {chunk.text}".strip()


def _passages(result: dict) -> list[dict]:
    """Group consecutive turns into passages worth retrieving on their own."""
    passages: list[dict] = []
    for turn in result.get("segments", []):
        if not turn.get("text"):
            continue
        current = passages[-1] if passages else None
        if current and turn["end"] - current["start"] <= PASSAGE_SECONDS:
            current["end"] = turn["end"]
            current["text"] += " " + turn["text"]
            speakers = dict.fromkeys([*current["speakers"].split(", "), turn.get("speaker", "")])
            current["speakers"] = ", ".join(filter(None, speakers))
        else:
            passages.append({
                "start": turn["start"],
                "end": turn["end"],
                "speakers": turn.get("speaker") or "",
                "text": turn["text"],
            })
    return passages


def _overlap(text: str, words: set[str]) -> float:
    lowered = text.lower()
    return sum(word in lowered for word in words) / max(len(words), 1)


def _unit(matrix: np.ndarray) -> np.ndarray:
    return matrix / np.linalg.norm(matrix, axis=1, keepdims=True).clip(1e-9)
