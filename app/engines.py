"""The three models, loaded once and shared by every worker thread."""

import logging
import os
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
    """Where CTranslate2 runs Whisper. It speaks CUDA or CPU and nothing else."""
    if settings.device != "auto":
        return settings.device
    return "cuda" if torch.cuda.is_available() else "cpu"


def torch_device() -> str:
    """Where pyannote runs. Being PyTorch, it also reaches Apple Silicon — measured at
    10.5x realtime against 1.8x on the same CPU, which is 4 minutes on a meeting
    instead of 22. Naming a device explicitly still wins, for a box where MPS misbehaves.
    """
    if settings.device != "auto":
        return settings.device
    if torch.cuda.is_available():
        return "cuda"
    return "mps" if torch.backends.mps.is_available() else "cpu"


def threads() -> int:
    """CTranslate2 takes 4 threads unless told otherwise, however many the box has.

    Measured on an M3 Max (12 performance cores): 3.2x realtime at the default, 4.4x
    with all of them. Workers share the machine, so each gets an equal slice.
    """
    return max((os.cpu_count() or 4) // settings.concurrency, 1)


def batching() -> bool:
    """Batched decoding is worth it on a CPU too: 4.4x realtime without it, 9.4x with.

    It costs memory, which is what `MT_BATCH_SIZE=1` is for.
    """
    return settings.batch_size > 1


@lru_cache(maxsize=2)
def _model(name: str) -> WhisperModel:
    dev = device()
    compute_type = settings.compute_type or ("float16" if dev == "cuda" else "int8")
    with _loading(name, f"{dev}, {compute_type}, {threads()} threads"):
        return WhisperModel(name, device=dev, compute_type=compute_type, cpu_threads=threads())


@lru_cache(maxsize=1)
def asr() -> WhisperModel | BatchedInferencePipeline:
    model = _model(settings.whisper_model)
    return BatchedInferencePipeline(model) if batching() else model


@lru_cache(maxsize=1)
def live_asr() -> WhisperModel:
    """One short utterance at a time, so batching buys nothing — measured, it is a wash.

    A smaller model does buy something: Whisper's encoder runs a 30-second window
    whatever you feed it, so a two-second sentence costs what a full one does.
    `MT_LIVE_MODEL` is that trade; the recording is kept either way.
    """
    return _model(settings.live_model or settings.whisper_model)


@lru_cache(maxsize=1)
def diarizer() -> Pipeline:
    """The weights are gated: a token alone is not enough, the account must be approved."""
    with _loading(settings.diarization_model, torch_device()):
        try:
            pipeline = Pipeline.from_pretrained(settings.diarization_model, token=settings.hf_token)
        except Exception as exc:
            raise RuntimeError(_no_weights(exc)) from exc
    if pipeline is None:
        raise RuntimeError(_no_weights("Hugging Face returned no pipeline"))
    return pipeline.to(torch.device(torch_device()))


def _no_weights(why: object) -> str:
    """Lead with the fix: this failure is always something a person has to go and click.

    A fresh token does not help — the *account* has to accept, and the pipeline pulls a
    second gated repo of its own, which is the half people miss.
    """
    gated = f"{settings.diarization_model} and pyannote/segmentation-3.0"
    return (
        f"Speaker diarization needs approved access to both {gated}. Signed in as the account "
        "that owns MT_HF_TOKEN, open each page and press Agree, then restart. Transcription "
        f"without speakers needs no token. ({str(why).splitlines()[0]})"
    )


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
