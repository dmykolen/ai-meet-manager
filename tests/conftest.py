import os
import shutil
import struct
import subprocess
import tempfile
import wave
from pathlib import Path

import pytest

TMP = Path(tempfile.mkdtemp(prefix="meeting-transcriber-tests-"))
os.environ |= {
    "MT_DATABASE_URL": f"sqlite:///{TMP}/jobs.db",
    "MT_MEDIA_DIR": str(TMP / "media"),
    "MT_POLL_INTERVAL": "0.05",
    "MT_LLM_API_KEY": "",
}

from fastapi.testclient import TestClient  # noqa: E402

from app import search  # noqa: E402
from app.main import app  # noqa: E402


@pytest.fixture(scope="session")
def client():
    with TestClient(app) as test_client:
        yield test_client


@pytest.fixture(autouse=True)
def offline_search(monkeypatch):
    """No embedding provider in tests, so search falls back to keywords."""
    monkeypatch.setattr(search, "embed", lambda texts: [])


@pytest.fixture(scope="session")
def anyio_backend() -> str:
    return "asyncio"


@pytest.fixture(scope="session")
def wav_bytes() -> bytes:
    """Two seconds of 44.1 kHz stereo audio, written with the standard library."""
    path = TMP / "sample.wav"
    with wave.open(str(path), "wb") as out:
        out.setnchannels(2)
        out.setsampwidth(2)
        out.setframerate(44100)
        out.writeframes(struct.pack(f"<{44100 * 4}h", *([1000, -1000] * 44100 * 2)))
    return path.read_bytes()


def encode(name: str, *arguments: str) -> Path:
    """Build a media fixture with ffmpeg, skipping the test when it is missing."""
    if not shutil.which("ffmpeg"):
        pytest.skip("ffmpeg not installed")
    target = TMP / name
    if not target.exists():
        subprocess.run(["ffmpeg", "-v", "error", "-y", *arguments, str(target)], check=True)
    return target


@pytest.fixture(scope="session")
def video_bytes() -> bytes:
    arguments = (
        "-f lavfi -i testsrc=size=320x240:rate=15:duration=3 "
        "-f lavfi -i sine=frequency=400:duration=3 "
        "-c:v libx264 -pix_fmt yuv420p -c:a aac -shortest"
    )
    return encode("clip.mp4", *arguments.split()).read_bytes()


@pytest.fixture(scope="session")
def silent_video_bytes() -> bytes:
    arguments = "-f lavfi -i testsrc=size=160x120:rate=10:duration=2 -an -c:v libx264"
    return encode("silent.mp4", *arguments.split(), "-pix_fmt", "yuv420p").read_bytes()
