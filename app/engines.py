"""The three models, loaded once and shared by every worker thread.

Which Whisper runs is decided by what the machine has, not by configuration: see
`backend()`. Whatever wins, it is handed to the pipeline wearing the same signature.
"""

import importlib.util
import logging
import math
import os
import platform
import time
from contextlib import contextmanager
from functools import lru_cache
from types import SimpleNamespace

import numpy as np
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


def backend() -> str:
    """Which Whisper this machine should run. Measured, not guessed — see RESEARCH.md.

    CUDA goes to CTranslate2: float16 there is both the fastest and the best proven.
    Apple Silicon goes to MLX, measured at 23.5x realtime against 9.7x for CTranslate2
    on the same clip and the same weights. Everything else — Linux and Windows on CPU —
    stays on CTranslate2, which is what is deployed there and what has been measured
    there. MLX does ship Linux wheels now; when somebody measures them on a real box,
    this function is the only thing that has to change.

    Parakeet is never chosen automatically. It transcribes Ukrainian far better —
    5.10% WER against 12.52% on FLEURS, and measured here at 78x realtime with no
    repeated lines where Whisper looped five times over a quiet stretch — but it is
    Apple Silicon only for now and reports no detected language, so it is opted into
    with MT_ASR_BACKEND=parakeet rather than sprung on anybody.
    """
    if settings.asr_backend != "auto":
        return settings.asr_backend
    if device() == "cuda":
        return "faster-whisper"
    return "mlx" if mlx_installed() else "faster-whisper"


def asr_device() -> str:
    """What the chosen Whisper actually runs on. `device()` answers for CTranslate2 only,
    and would report "cpu" on a machine where MLX is the one doing the work."""
    return "metal" if backend() in ("mlx", "parakeet") else device()


def mlx_installed() -> bool:
    """Apple Silicon with the optional extra actually installed (`uv sync --extra mlx`)."""
    return _apple_silicon("mlx_whisper")


def parakeet_installed() -> bool:
    """Apple Silicon with `uv sync --extra parakeet`."""
    return _apple_silicon("parakeet_mlx")


def _apple_silicon(module: str) -> bool:
    return platform.system() == "Darwin" and platform.machine() == "arm64" and importlib.util.find_spec(module) is not None


class Mlx:
    """mlx-whisper wearing faster-whisper's signature, so the pipeline sees one shape."""

    # MLX chunks on its own and does not batch. `beam_size` is not merely unsupported:
    # it raises NotImplementedError even when set to 1, and MLX decodes greedily anyway,
    # so dropping it is what honours the caller's intent rather than ignoring it.
    IGNORED = ("batch_size", "vad_filter", "beam_size")

    def __init__(self, repo: str):
        self.repo = repo

    def transcribe(self, audio: np.ndarray, **options):
        import mlx_whisper

        asked = {k: v for k, v in options.items() if k not in self.IGNORED and v is not None}
        if hotwords := asked.pop("hotwords", None):
            asked["initial_prompt"] = hotwords  # the nearest thing MLX has to a vocabulary
        spoken = mlx_whisper.transcribe(audio, path_or_hf_repo=self.repo, **asked)
        segments = [_as_segment(raw) for raw in spoken["segments"]]
        return segments, SimpleNamespace(language=spoken["language"])


def _as_segment(raw: dict) -> SimpleNamespace:
    """MLX already reports everything the pipeline reads; it just reports it as dicts."""
    spoken = raw.get("words") or []
    words = [SimpleNamespace(word=w["word"], start=w["start"], end=w["end"]) for w in spoken]
    return SimpleNamespace(
        text=raw["text"],
        start=raw["start"],
        end=raw["end"],
        words=words or None,
        avg_logprob=raw["avg_logprob"],
        no_speech_prob=raw["no_speech_prob"],
    )


class Parakeet:
    """NVIDIA's Parakeet TDT v3 wearing faster-whisper's signature.

    Why it is here at all: it is a transducer, not an autoregressive decoder. It
    emits a blank per frame when nobody is talking, where Whisper invents fluent
    text — fourteen identical rows of a stock Russian subtitle phrase over one real
    meeting's silence, at confidences of 0.75 to 0.90, which no confidence filter
    could ever have caught. And on Ukrainian, which is what most of these meetings
    are in, it is roughly two and a half times more accurate.

    Everything Whisper is asked for that Parakeet has no notion of is dropped: it
    decodes greedily by construction, chunks on its own, and does not take a
    vocabulary or a language.
    """

    IGNORED = ("batch_size", "vad_filter", "beam_size", "hotwords", "language", "condition_on_previous_text")

    # Long meetings are chunked rather than fed whole, with an overlap wide enough
    # that a sentence spanning the seam is not cut in half.
    CHUNK = 600.0
    OVERLAP = 15.0

    def __init__(self, repo: str):
        self.repo = repo
        self._model = None

    def _load(self):
        if self._model is None:
            from parakeet_mlx import from_pretrained

            self._model = from_pretrained(self.repo)
        return self._model

    def transcribe(self, audio: np.ndarray, **options):
        import mlx.core as mx
        from parakeet_mlx.audio import get_logmel

        model = self._load()
        mel = get_logmel(mx.array(audio), model.preprocessor_config)
        spoken = model.generate(mel)[0]
        segments = [_from_sentence(s) for s in spoken.sentences]
        return segments, SimpleNamespace(language=settings.language or _alphabet(spoken.text))


# Letters that exist in one of these alphabets and not the others. Parakeet v3
# identifies the language internally and then does not report it, and a fixed
# MT_LANGUAGE would be wrong for anybody whose meetings are not all in one
# language. This is not language detection in general — it is the smallest thing
# that separates the three that turn up here, and it says nothing when unsure.
ONLY_UKRAINIAN = set("їієґ")
ONLY_RUSSIAN = set("ыэъё")


def _alphabet(text: str) -> str:
    lowered = text.lower()
    uk = sum(lowered.count(c) for c in ONLY_UKRAINIAN)
    ru = sum(lowered.count(c) for c in ONLY_RUSSIAN)
    if uk or ru:
        return "uk" if uk >= ru else "ru"
    cyrillic = sum("\u0400" <= c <= "\u04ff" for c in lowered)
    if cyrillic:
        return ""  # Cyrillic, but nothing that tells the two apart
    return "en" if any(c.isalpha() for c in lowered) else ""


def _from_sentence(sentence) -> SimpleNamespace:
    """One Parakeet sentence as the pipeline expects a Whisper segment."""
    return SimpleNamespace(
        text=sentence.text,
        start=sentence.start,
        end=sentence.end,
        words=_words(sentence) or None,
        # A probability where Whisper reports a log probability; `_confidence`
        # exponentiates it back, so this keeps the two engines comparable.
        avg_logprob=math.log(max(sentence.confidence, 1e-9)),
        # Nothing is emitted over silence in the first place, so there is no
        # "this was probably not speech" to report.
        no_speech_prob=0.0,
    )


def _words(sentence) -> list[SimpleNamespace]:
    """Sub-word tokens joined back into words, which is what diarization aligns on.

    Parakeet tokenises sub-word: " to", "ken", " is", "ed". A leading space starts
    a new word, which is the SentencePiece convention and the only rule needed here.
    """
    words: list[SimpleNamespace] = []
    for token in sentence.tokens:
        if words and not token.text.startswith(" "):
            words[-1].word += token.text
            words[-1].end = token.end
            continue
        words.append(SimpleNamespace(word=token.text, start=token.start, end=token.end))
    return words


PARAKEET_WEIGHTS = {"parakeet": "mlx-community/parakeet-tdt-0.6b-v3"}


@lru_cache(maxsize=1)
def _parakeet(name: str) -> Parakeet:
    repo = name if "/" in name else PARAKEET_WEIGHTS.get(name, PARAKEET_WEIGHTS["parakeet"])
    engine = Parakeet(repo)
    with _loading(repo, "parakeet, apple silicon"):
        engine.transcribe(np.zeros(16000, dtype=np.float32))  # fetch the weights now, not mid-job
    return engine


# MLX keeps the weights in its own layout, and mlx-community names those repositories
# by no rule at all — `-mlx` for most sizes, bare for turbo, and the `-fp16` ones are
# not MLX weights however much they look like it. So the map is written out.
MLX_WEIGHTS = {
    "large-v3-turbo": "mlx-community/whisper-large-v3-turbo",
    "large-v3": "mlx-community/whisper-large-v3-mlx",
    "medium": "mlx-community/whisper-medium-mlx",
    "small": "mlx-community/whisper-small-mlx",
    "base": "mlx-community/whisper-base-mlx",
    "tiny": "mlx-community/whisper-tiny-mlx",
}


@lru_cache(maxsize=2)
def _mlx(name: str) -> Mlx:
    repo = name if "/" in name else MLX_WEIGHTS.get(name)
    if repo is None:
        raise LookupError(f"no MLX weights are mapped for {name!r}; name a repository instead")
    engine = Mlx(repo)
    with _loading(repo, "mlx, apple silicon"):
        engine.transcribe(np.zeros(16000, dtype=np.float32))  # fetch the weights now, not mid-job
    return engine


def _engine(name: str, batched: bool):
    """One model name on whichever backend this machine wants, falling back if it cannot."""
    if backend() == "parakeet":
        try:
            return _parakeet(name)
        except Exception as exc:
            log.warning("Parakeet could not load (%s); using Whisper instead", exc)
    if backend() == "mlx":
        try:
            return _mlx(name)
        except Exception as exc:
            log.warning("MLX could not load %s (%s); using CTranslate2 instead", name, exc)
    model = _model(name)
    return BatchedInferencePipeline(model) if batched else model


@lru_cache(maxsize=2)
def _model(name: str) -> WhisperModel:
    dev = device()
    compute_type = settings.compute_type or ("float16" if dev == "cuda" else "int8")
    with _loading(name, f"{dev}, {compute_type}, {threads()} threads"):
        return WhisperModel(name, device=dev, compute_type=compute_type, cpu_threads=threads())


@lru_cache(maxsize=1)
def asr():
    return _engine(settings.whisper_model, batched=batching())


@lru_cache(maxsize=1)
def live_asr():
    """One short utterance at a time, so batching buys nothing — measured, it is a wash.

    A smaller model does buy something: Whisper's encoder runs a 30-second window
    whatever you feed it, so a two-second sentence costs what a full one does.
    `MT_LIVE_MODEL` is that trade; the recording is kept either way.
    """
    return _engine(settings.live_model or settings.whisper_model, batched=False)


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
