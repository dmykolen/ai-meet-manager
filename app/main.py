import logging
import threading
import time
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from fastapi.staticfiles import StaticFiles

from app import worker
from app.api import routers
from app.config import settings
from app.models import init_db, model_cache

log = logging.getLogger(__name__)

# Loading PyTorch, pyannote and Whisper takes seconds on a warm machine and minutes
# on a cold one, so it happens in the background while the API is already serving.
runtime = {"device": "resolving", "models": "not loaded"}


def warm(preload: bool) -> None:
    from app import engines  # deferred so the server binds its port immediately

    runtime["device"] = engines.device()
    log.info("Device: %s (batching %s)", runtime["device"], engines.batching())
    if not preload:
        runtime["models"] = "loaded on first use"
        return
    started = time.monotonic()
    log.info("Loading models. The first run downloads several GB into %s", model_cache())
    try:
        engines.warm_up()
        runtime["models"] = "ready"
        log.info("Models ready in %.0fs", time.monotonic() - started)
    except Exception as exc:
        runtime["models"] = f"unavailable: {exc}"
        log.error("Models could not be loaded: %s", exc)


@asynccontextmanager
async def lifespan(_: FastAPI):
    logging.basicConfig(level=logging.INFO)
    init_db()
    log.info("Database %s, media in %s", settings.database_url, settings.media_dir.resolve())
    log.info("Model cache %s", model_cache())
    threading.Thread(target=warm, args=(settings.preload_models,), daemon=True).start()
    if settings.role in ("all", "worker"):
        worker.expire_media()
        worker.start()
    log.info("Ready as %s", settings.role)
    yield
    worker.stop()


app = FastAPI(
    title="Meeting Transcriber",
    description="Transcription, diarization, search and summaries for workplace recordings.",
    version="2.1.0",
    lifespan=lifespan,
)
app.add_middleware(
    CORSMiddleware,
    allow_origins=settings.cors_origins,
    allow_methods=["*"],
    allow_headers=["*"],
)
for router in routers:
    app.include_router(router)


@app.get("/health", tags=["ops"])
def health() -> dict:
    """Liveness. Answers immediately, whether or not the models have loaded."""
    return {
        "status": "ok",
        "role": settings.role,
        "device": runtime["device"],
        "models": runtime["models"],
        "model_cache": str(model_cache()),
        "whisper_model": settings.whisper_model,
        "diarization_model": settings.diarization_model,
        "llm_model": f"{settings.llm_provider}/{settings.llm_model}",
        "keep_media_days": settings.keep_media_days,
    }


@app.get("/health/ready", tags=["ops"])
def ready() -> dict:
    """Readiness: loads the models if they are not loaded, so jobs cannot fail on arrival."""
    from app import engines

    try:
        engines.warm_up()
    except Exception as exc:
        raise HTTPException(503, f"Models unavailable: {exc}") from exc
    runtime["models"] = "ready"
    return {"status": "ready", "device": engines.device()}


app.mount("/", StaticFiles(directory=Path(__file__).parent.parent / "web", html=True))
