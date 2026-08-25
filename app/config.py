import os
from pathlib import Path
from typing import Literal

from pydantic import field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """Configuration, overridable via environment variables or a .env file."""

    model_config = SettingsConfigDict(env_prefix="MT_", env_file=".env", extra="ignore")

    # --- storage and roles ---
    database_url: str = "sqlite:///data/jobs.db"
    media_dir: Path = Path("data/media")
    # "all" runs the API and a worker in one process. Split the two to scale
    # workers horizontally; they then need a shared database and media volume.
    role: Literal["all", "api", "worker"] = "all"
    poll_interval: float = 2.0
    # A job whose worker stopped sending heartbeats for this long is taken over.
    lease_seconds: int = 300

    # --- models ---
    device: Literal["auto", "cpu", "cuda"] = "auto"  # "auto" = CUDA when present
    compute_type: str = ""  # empty: float16 on GPU, int8 on CPU
    # Where downloaded weights live. Point it at a volume so they survive a rebuild;
    # leave it empty for the Hugging Face default (~/.cache/huggingface).
    model_cache: Path | None = None
    # Which Whisper implementation runs. "auto" picks per machine, see engines.backend().
    asr_backend: Literal["auto", "faster-whisper", "mlx"] = "auto"
    whisper_model: str = "large-v3-turbo"
    live_model: str = ""  # blank means whisper_model; a smaller one cuts live latency
    diarization_model: str = "pyannote/speaker-diarization-community-1"
    # Required for the gated pyannote models: https://huggingface.co/settings/tokens
    hf_token: str | None = None
    # Transcribe several windows at once. Needs spare VRAM; ignored on CPU.
    batch_size: int = 8
    preload_models: bool = False  # load at startup instead of on first job

    # --- transcription quality ---
    language: str | None = None  # None = auto-detect
    vocabulary: str = ""  # names, products and jargon to expect
    # Silence never reaches the models: one VAD pass feeds both of them.
    trim_silence: bool = True
    # Whisper invents text over silence. Drop segments that look like that.
    drop_no_speech_above: float = 0.8
    drop_logprob_below: float = -1.0

    # --- speakers ---
    # Cosine similarity above which two voice samples count as the same person.
    speaker_match_threshold: float = 0.55

    # --- summaries, search and sharing ---
    llm_provider: Literal["openai", "anthropic"] = "openai"
    llm_model: str = "gpt-5-mini"
    llm_api_key: str | None = None  # falls back to OPENAI_API_KEY / ANTHROPIC_API_KEY
    embedding_model: str = "text-embedding-3-small"  # OpenAI; needed for search
    search_results: int = 8
    share_days: int = 14

    # --- limits ---
    max_upload_mb: int = 4096
    concurrency: int = 1  # jobs at once per worker process
    max_attempts: int = 3
    keep_media_days: int = 7  # 0 deletes recordings as soon as the job ends
    cors_origins: list[str] = ["*"]

    @field_validator("language", "hf_token", "llm_api_key", "model_cache", mode="before")
    @classmethod
    def _blank_is_unset(cls, value: str | None) -> str | None:
        """A blank value in a .env file means "not set", not an empty string or path."""
        return value or None


settings = Settings()

# Hugging Face reads this when its libraries are first imported, so it has to be set
# before anything touches them. Importing app.config first is what makes that true.
if settings.model_cache:
    os.environ.setdefault("HF_HOME", str(settings.model_cache.expanduser().resolve()))
