"""The three models, loaded once and shared by every worker thread."""

import logging
import time
from contextlib import contextmanager
from functools import lru_cache

import torch
from faster_whisper import BatchedInferencePipeline, WhisperModel
from pyannote.audio import Pipeline

from app.config import settings
from app.models import cached_gigabytes, model_cache

log = logging.getLogger(__name__)


def device() -> str:
    if settings.device != "auto":
        return settings.device
    return "cuda" if torch.cuda.is_available() else "cpu"


def batching() -> bool:
    """Batched decoding pays off on a GPU and costs memory on a CPU."""
    return settings.batch_size > 1 and device() == "cuda"


@lru_cache(maxsize=1)
def asr() -> WhisperModel | BatchedInferencePipeline:
    dev = device()
    compute_type = settings.compute_type or ("float16" if dev == "cuda" else "int8")
    with _loading(settings.whisper_model, f"{dev}, {compute_type}"):
        model = WhisperModel(settings.whisper_model, device=dev, compute_type=compute_type)
    return BatchedInferencePipeline(model) if batching() else model


@lru_cache(maxsize=1)
def diarizer() -> Pipeline:
    with _loading(settings.diarization_model, device()):
        pipeline = Pipeline.from_pretrained(settings.diarization_model, token=settings.hf_token)
    if pipeline is None:
        raise RuntimeError(
            f"Could not load {settings.diarization_model}. Accept the model's conditions on "
            "Hugging Face and set MT_HF_TOKEN to a token with read access."
        )
    return pipeline.to(torch.device(device()))


@contextmanager
def _loading(name: str, where: str):
    """Say what is being fetched and from where, because the first run is slow."""
    started, cached = time.monotonic(), cached_gigabytes()
    log.info("Loading %s (%s). Cache %s holds %.2f GB", name, where, model_cache(), cached)
    yield
    grew = cached_gigabytes() - cached
    log.info(
        "Loaded %s in %.0fs%s",
        name,
        time.monotonic() - started,
        f", downloaded {grew:.2f} GB" if grew > 0.01 else " from cache",
    )


def embedder():
    """The speaker-embedding half of the diarization pipeline, reused on its own."""
    return diarizer()._embedding


def warm_up() -> None:
    asr()
    diarizer()
