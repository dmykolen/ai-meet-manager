"""Decoding and silence removal.

Anything FFmpeg can open is accepted, video included: PyAV ships with
faster-whisper, picks the audio stream out of the container and hands back a
16 kHz mono waveform, so no ffmpeg binary and no intermediate files are needed.

The same waveform then goes through a single Silero VAD pass. Only speech is
kept, so neither Whisper nor pyannote ever sees the quiet parts of a meeting,
and every timestamp they produce is mapped back onto the original recording.
"""

import logging
from collections.abc import Callable
from pathlib import Path

import av
import numpy as np
from faster_whisper.audio import decode_audio
from faster_whisper.vad import (
    SpeechTimestampsMap,
    VadOptions,
    collect_chunks,
    get_speech_timestamps,
)

from app.config import settings

SAMPLE_RATE = 16000
VAD = VadOptions(min_silence_duration_ms=1000, speech_pad_ms=300)
log = logging.getLogger(__name__)

Remap = Callable[[float], float]


class UnplayableMedia(Exception):
    """The upload has no audio track, or no decoder for it."""


def decode(path: Path) -> np.ndarray:
    """The audio of any container, as 16 kHz mono float32."""
    try:
        return decode_audio(str(path), sampling_rate=SAMPLE_RATE)
    except Exception as exc:
        raise UnplayableMedia(f"No decodable audio track in {path.name}") from exc


def has_video(path: Path) -> bool:
    try:
        with av.open(str(path)) as container:
            return bool(container.streams.video)
    except av.FFmpegError:
        return False


def speech_only(audio: np.ndarray) -> tuple[np.ndarray, Remap]:
    """Drop the silence, and return a way back to the original timeline."""
    if not settings.trim_silence:
        return audio, lambda seconds: seconds
    speech = get_speech_timestamps(audio, VAD)
    if not speech:
        return audio, lambda seconds: seconds
    kept, _ = collect_chunks(audio, speech, SAMPLE_RATE)
    compact = np.concatenate(kept)
    log.info(
        "Trimmed %.0fs of silence from %.0fs of audio",
        (len(audio) - len(compact)) / SAMPLE_RATE,
        len(audio) / SAMPLE_RATE,
    )
    return compact, SpeechTimestampsMap(speech, SAMPLE_RATE).get_original_time
