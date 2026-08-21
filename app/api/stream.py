import logging
from contextlib import suppress

from fastapi import APIRouter, WebSocket
from sqlmodel import Session
from starlette.websockets import WebSocketDisconnect

from app.media import SAMPLE_RATE
from app.models import Job, Status, Task, engine, now
from app.voices import everyone

router = APIRouter(prefix="/v1", tags=["live"])
log = logging.getLogger(__name__)


@router.websocket("/stream")
async def stream(socket: WebSocket, vocabulary: str | None = None, identify: bool = False) -> None:
    """Live transcription of 16 kHz mono float32 PCM sent as binary frames.

    The stream is recorded and kept as an ordinary job, so a live meeting ends up
    with the same playback, summary, search and analytics as an uploaded one.
    Send the text `stop` to finish; the closing message carries the whole
    transcript, including the sentence that was still being spoken.
    """
    from app.live import LiveSession  # deferred: see app/worker.py

    await socket.accept()
    with Session(engine) as session:
        job = Job(
            task=Task.transcribe_diarize if identify else Task.transcribe,
            status=Status.running,
            live=True,
            filename=f"live {now():%Y-%m-%d %H:%M}.wav",
            content_type="audio/wav",
            suffix=".wav",
            heartbeat_at=now(),
        )
        session.add(job)
        session.commit()
        session.refresh(job)
    live = LiveSession(job, vocabulary, everyone(session) if identify else [])

    await socket.send_json({"type": "ready", "sample_rate": SAMPLE_RATE, "job_id": str(job.id)})
    failure = None
    try:
        while True:
            message = await socket.receive()
            if chunk := message.get("bytes"):
                live.add(chunk)
                for turn in await live.drain():
                    await socket.send_json({"type": "turn", **turn})
            elif message.get("text") == "stop" or message["type"] == "websocket.disconnect":
                break
    except WebSocketDisconnect:
        pass
    except Exception as exc:
        failure = str(exc)
        log.exception("Live session failed")
        await _tell(socket, {"type": "error", "detail": failure})

    finished = await live.finish(failure)
    await _tell(socket, {"type": "done", "job_id": str(job.id), "result": finished.result})


async def _tell(socket: WebSocket, message: dict) -> None:
    """The client may already be gone by the time a session wraps up."""
    with suppress(WebSocketDisconnect, RuntimeError):
        await socket.send_json(message)
