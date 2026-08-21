from typing import Annotated

from fastapi import APIRouter, Body, HTTPException, Query
from starlette.concurrency import run_in_threadpool

from app import insights, search
from app.api.deps import SessionDep

router = APIRouter(prefix="/v1", tags=["search"])


@router.get("/search")
def find(session: SessionDep, q: Annotated[str, Query(min_length=2)]) -> list[dict]:
    """Passages from past meetings, ranked by meaning where embeddings are available."""
    return [
        {
            "job_id": str(job.id),
            "filename": job.filename,
            "start": chunk.start,
            "end": chunk.end,
            "speakers": chunk.speakers,
            "text": chunk.text,
        }
        for chunk, job in search.find(session, q)
    ]


@router.post("/ask")
async def ask(session: SessionDep, question: Annotated[str, Body(embed=True)]) -> dict:
    """An answer drawn from the meetings that mention it, with the passages used."""
    hits = search.find(session, question)
    if not hits:
        raise HTTPException(404, "Nothing in the transcripts covers that")
    passages = [search.passage_label(chunk, job) for chunk, job in hits]
    try:
        reply = await run_in_threadpool(insights.answer, question, passages)
    except Exception as exc:
        raise HTTPException(502, f"Could not answer: {exc}") from exc
    return {
        "answer": reply,
        "sources": [
            {"job_id": str(job.id), "filename": job.filename, "start": chunk.start}
            for chunk, job in hits
        ],
    }
