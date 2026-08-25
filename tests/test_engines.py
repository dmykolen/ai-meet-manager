"""Choosing a Whisper implementation, and making the chosen one look like the other."""

import sys
from types import SimpleNamespace
from typing import ClassVar

import numpy as np
import pytest

from app import engines
from app.config import settings

SPOKEN = {
    "language": "uk",
    "segments": [
        {
            "text": " Привіт.",
            "start": 0.0,
            "end": 1.2,
            "avg_logprob": -0.2,
            "no_speech_prob": 0.05,
            "words": [{"word": " Привіт.", "start": 0.0, "end": 1.2, "probability": 0.9}],
        }
    ],
}


class FakeMlx:
    """Stands in for mlx_whisper, and refuses what the real one refuses."""

    calls: ClassVar[list[dict]] = []

    @staticmethod
    def transcribe(audio, **options):
        for absent in ("hotwords", "batch_size", "vad_filter"):
            if absent in options:  # as mlx_whisper does, having no such parameter
                raise TypeError(f"transcribe() got an unexpected keyword argument {absent!r}")
        FakeMlx.calls.append({k: v for k, v in options.items() if k != "path_or_hf_repo"})
        return SPOKEN


@pytest.fixture
def fake_mlx(monkeypatch):
    FakeMlx.calls.clear()
    monkeypatch.setitem(sys.modules, "mlx_whisper", FakeMlx)
    return FakeMlx


def on(monkeypatch, *, system="Darwin", machine="arm64", cuda=False, installed=True):
    monkeypatch.setattr(engines.platform, "system", lambda: system)
    monkeypatch.setattr(engines.platform, "machine", lambda: machine)
    monkeypatch.setattr(engines.torch.cuda, "is_available", lambda: cuda)
    found = object() if installed else None
    monkeypatch.setattr(engines.importlib.util, "find_spec", lambda _name: found)
    monkeypatch.setattr(settings, "device", "auto")
    monkeypatch.setattr(settings, "asr_backend", "auto")


# --- which backend ------------------------------------------------------------


def test_apple_silicon_uses_mlx_when_the_extra_is_installed(monkeypatch):
    on(monkeypatch)
    assert engines.backend() == "mlx"


def test_a_mac_without_the_extra_stays_on_ctranslate2(monkeypatch):
    on(monkeypatch, installed=False)
    assert engines.backend() == "faster-whisper"


def test_an_intel_mac_stays_on_ctranslate2(monkeypatch):
    on(monkeypatch, machine="x86_64")
    assert engines.backend() == "faster-whisper"


def test_linux_stays_on_ctranslate2(monkeypatch):
    """MLX ships Linux wheels now, but nobody has measured them on a real box."""
    on(monkeypatch, system="Linux", machine="x86_64", installed=False)
    assert engines.backend() == "faster-whisper"


def test_a_gpu_wins_over_apple_silicon(monkeypatch):
    """float16 on CUDA is both the fastest and the best proven, so it is never displaced."""
    on(monkeypatch, cuda=True)
    assert engines.backend() == "faster-whisper"


def test_naming_a_backend_beats_the_machine(monkeypatch):
    on(monkeypatch, system="Linux", machine="x86_64", installed=False)
    monkeypatch.setattr(settings, "asr_backend", "mlx")
    assert engines.backend() == "mlx"


# --- the adapter --------------------------------------------------------------


def test_the_vocabulary_becomes_the_only_thing_mlx_has_for_one(fake_mlx):
    engines.Mlx("repo").transcribe(np.zeros(16000, dtype=np.float32), hotwords="Kubernetes, Priya")
    assert fake_mlx.calls[-1]["initial_prompt"] == "Kubernetes, Priya"


def test_options_meant_for_ctranslate2_are_not_forwarded(fake_mlx):
    """MLX chunks on its own and has no batching, and would raise on either word."""
    engines.Mlx("repo").transcribe(
        np.zeros(16000, dtype=np.float32), batch_size=8, vad_filter=True, language="uk"
    )
    assert fake_mlx.calls[-1] == {"language": "uk"}


def test_unset_options_are_left_out_so_mlx_keeps_its_own_defaults(fake_mlx):
    engines.Mlx("repo").transcribe(np.zeros(16000, dtype=np.float32), language=None, hotwords=None)
    assert fake_mlx.calls[-1] == {}


def test_segments_come_back_in_the_shape_the_pipeline_reads(fake_mlx):
    segments, info = engines.Mlx("repo").transcribe(np.zeros(16000, dtype=np.float32))
    assert info.language == "uk"
    turn = segments[0]
    assert (turn.text, turn.start, turn.end) == (" Привіт.", 0.0, 1.2)
    assert (turn.avg_logprob, turn.no_speech_prob) == (-0.2, 0.05)
    assert [(w.word, w.start, w.end) for w in turn.words] == [(" Привіт.", 0.0, 1.2)]


def test_a_segment_without_words_reports_none_like_faster_whisper_does(fake_mlx):
    monkeyed = {**SPOKEN, "segments": [{**SPOKEN["segments"][0], "words": []}]}
    monkeypatched = SimpleNamespace(transcribe=lambda audio, **kw: monkeyed)
    sys.modules["mlx_whisper"] = monkeypatched
    segments, _ = engines.Mlx("repo").transcribe(np.zeros(16000, dtype=np.float32))
    assert segments[0].words is None, "the pipeline tests `if not chunk.words`"


def test_a_plain_model_name_is_resolved_to_the_mlx_weights():
    assert engines.Mlx("large-v3-turbo").repo == "large-v3-turbo"  # only _mlx() maps it


@pytest.mark.parametrize(
    ("name", "repo"),
    [
        ("large-v3-turbo", "mlx-community/whisper-large-v3-turbo"),
        ("small", "mlx-community/whisper-small-mlx"),
        ("mlx-community/whatever", "mlx-community/whatever"),  # already a repository id
    ],
)
def test_model_names_map_onto_the_mlx_repositories(name, repo, fake_mlx):
    engines._mlx.cache_clear()
    assert engines._mlx(name).repo == repo


def test_an_unmapped_model_falls_back_instead_of_guessing_a_repository(monkeypatch, fake_mlx):
    """Guessing produced mlx-community/whisper-...-fp16, which exists and is not MLX weights."""
    on(monkeypatch)
    engines._mlx.cache_clear()
    with pytest.raises(LookupError):
        engines._mlx("distil-large-v3")
