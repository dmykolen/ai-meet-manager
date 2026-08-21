import numpy as np
import pytest

from app import media
from app.media import SAMPLE_RATE, UnplayableMedia, decode, has_video, speech_only
from tests.conftest import TMP


def write(name: str, data: bytes):
    path = TMP / name
    path.write_bytes(data)
    return path


def test_audio_is_extracted_from_video(video_bytes):
    audio = decode(write("in.mp4", video_bytes))
    assert audio.ndim == 1 and audio.dtype == np.float32
    assert len(audio) / SAMPLE_RATE == pytest.approx(3.0, abs=0.2)


def test_video_is_recognised_as_video(video_bytes, wav_bytes):
    assert has_video(write("in2.mp4", video_bytes))
    assert not has_video(write("in.wav", wav_bytes))


def test_a_video_without_sound_is_refused(silent_video_bytes):
    with pytest.raises(UnplayableMedia):
        decode(write("mute.mp4", silent_video_bytes))


def test_a_file_that_is_not_media_is_refused():
    with pytest.raises(UnplayableMedia):
        decode(write("notes.txt", b"this is not a recording"))


# --- silence removal ----------------------------------------------------------


def sample() -> np.ndarray:
    quiet, loud = np.zeros(SAMPLE_RATE, np.float32), np.full(SAMPLE_RATE, 0.5, np.float32)
    return np.concatenate([quiet, loud, quiet, quiet, loud])


def test_silence_is_dropped_and_timestamps_still_point_at_the_original(monkeypatch):
    monkeypatch.setattr(
        media,
        "get_speech_timestamps",
        lambda audio, options=None: [
            {"start": SAMPLE_RATE, "end": 2 * SAMPLE_RATE},
            {"start": 4 * SAMPLE_RATE, "end": 5 * SAMPLE_RATE},
        ],
    )
    compact, remap = speech_only(sample())
    assert len(compact) / SAMPLE_RATE == 2.0  # five seconds in, two seconds out
    assert remap(0.0) == 1.0  # first speech really began a second in
    assert remap(1.0) == 4.0  # and the second utterance four seconds in


def test_audio_is_untouched_when_no_speech_is_found(monkeypatch):
    monkeypatch.setattr(media, "get_speech_timestamps", lambda audio, options=None: [])
    audio = sample()
    compact, remap = speech_only(audio)
    assert len(compact) == len(audio)
    assert remap(1.5) == 1.5


def test_trimming_can_be_switched_off(monkeypatch):
    monkeypatch.setattr(media.settings, "trim_silence", False)
    audio = sample()
    compact, remap = speech_only(audio)
    assert len(compact) == len(audio)
    assert remap(2.0) == 2.0
