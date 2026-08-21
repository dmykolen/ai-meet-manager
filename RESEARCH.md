# Stack selection

Reviewed August 2026. Every candidate below is open source, Python-usable and
was checked against its current documentation, repository and model card rather
than from memory.

## Transcription (ASR)

| # | Project | Model / engine | Quality | CPU | GPU | Maturity & maintenance | Integration cost |
|---|---------|----------------|---------|-----|-----|------------------------|------------------|
| 1 | **faster-whisper** (SYSTRAN) | Whisper `large-v3-turbo` on CTranslate2 | Whisper-level WER; word timestamps, Silero VAD, batching | **Yes** — INT8, 4× faster than reference Whisper | Yes — float16 / int8_float16 | v1.2.1, very widely deployed, clean typed API | Low: `pip install`, decodes audio itself through PyAV |
| 2 | openai-whisper | Whisper (PyTorch reference) | Same models | Very slow | Yes | Reference implementation, feature-frozen | Low, but no VAD/batching and much slower |
| 3 | WhisperX | faster-whisper + wav2vec2 alignment + pyannote | Best word alignment | Inherited | Yes | Active, but pins `pyannote-audio<4.0` and `torch~=2.8` | Medium — its pins block pyannote 4.x and the current diarizer |
| 4 | whisper.cpp / pywhispercpp | GGML Whisper | Whisper-level | Excellent on CPU/edge | Partial | Very active | Medium: C++ build, GGUF conversion, weaker Python API |
| 5 | NVIDIA NeMo — Parakeet TDT 0.6B v3 | Conformer/TDT | 6.3% avg WER, RTFx ≈ 3300, 25 European languages | Not recommended by the model card | Excellent | CC-BY-4.0, actively developed | High: NeMo is a large training framework, GPU-first |
| 6 | NVIDIA NeMo — Canary-Qwen 2.5B | Encoder + LLM decoder | ~5.6% WER, English only | No | Excellent | Active | High, English-only |
| 7 | Voxtral (Mistral) | 3B audio LLM | ~7.7% WER, 13 languages, streaming | Impractical | Yes | Apache 2.0, active | Medium–high, needs vLLM for reasonable throughput |
| 8 | Qwen3-ASR 0.6B / 1.7B | Transformer | ~5.8% WER, 52 languages | Marginal | Yes | Apache 2.0, new | Medium, timestamps only in 11 languages |
| 9 | sherpa-onnx (k2-fsa) | ONNX Runtime | Good | Excellent | Yes | Very active, bundles diarization too | Medium: model zoo juggling, lower ceiling on accuracy |
| 10 | Vosk / Mozilla DeepSpeech / Coqui STT | Kaldi / DeepSpeech | Well below Whisper | Yes | — | **DeepSpeech archived (2021), Coqui STT unmaintained**; Vosk alive but dated | Low, but the accuracy gap is disqualifying |

## Diarization

| # | Project | Approach | Quality | CPU | GPU | Notes |
|---|---------|----------|---------|-----|-----|-------|
| 1 | **pyannote.audio 4.x** + `speaker-diarization-community-1` | segmentation + embeddings + VBx clustering | Best open results at unbounded speaker counts (AMI-IHM 17.0% DER vs 18.8% for 3.1) | **Yes, by default** | `pipeline.to("cuda")` | v4.0.7, CC-BY-4.0 weights, gated behind a free HF token |
| 2 | pyannote 3.1 pipeline | AHC clustering | Superseded by community-1 | Yes | Yes | Only reason to use it is WhisperX compatibility |
| 3 | NVIDIA NeMo Sortformer | end-to-end neural diarizer | Competitive, very fast | GPU-first | Excellent | **Caps at 4 speakers** — too limiting for meetings |
| 4 | NeMo clustering / MSDD | TitaNet + clustering | Good, improves with Demucs pre-separation | Slow | Yes | Pulls in the whole NeMo framework |
| 5 | SpeechBrain | ECAPA-TDNN + spectral clustering | Good embeddings, no turnkey pipeline | Yes | Yes | You assemble VAD + embedding + clustering yourself |
| 6 | sherpa-onnx offline diarization | pyannote segmentation exported to ONNX | Slightly below native pyannote | Excellent | Yes | Good when PyTorch is unwanted |
| 7 | 3D-Speaker (Alibaba) | embeddings + clustering | Strong on Mandarin | Yes | Yes | Smaller community, thinner docs |
| 8 | diart | streaming/online diarization | Lower than offline | Yes | Yes | Built for live streams, not recorded meetings |
| 9 | simple-diarizer | spectral clustering | Basic | Yes | Yes | Small hobby project |
| 10 | Commercial APIs (AssemblyAI, Deepgram, pyannoteAI premium) | hosted | Best-in-class | n/a | n/a | Rejected: recordings must stay on-premise |

## Choice

**faster-whisper (`large-v3-turbo`) + pyannote.audio 4.x (`speaker-diarization-community-1`).**

* Both run on CPU and GPU from the same code — CTranslate2 switches to INT8 on
  CPU, pyannote is a plain PyTorch module moved with `.to(device)`. The only
  device-dependent code in this project is four lines in `app/pipeline.py`.
* Highest accuracy available under a permissive/attribution licence with no
  cap on speaker count, which rules out Sortformer for meetings.
* Both accept an in-memory 16 kHz mono waveform, so one decode with
  `faster_whisper.decode_audio` (PyAV, bundled FFmpeg) feeds both models: any
  input container, no temporary files, no system FFmpeg dependency.
* pyannote 4 returns `exclusive_speaker_diarization`, an overlap-free
  annotation built specifically for transcript alignment, plus
  `Annotation.argmax(segment)`. Assigning a speaker to every word therefore
  takes one library call rather than a custom overlap solver.
* WhisperX was the obvious "batteries included" alternative and was rejected:
  it pins `pyannote-audio<4.0` and `torch~=2.8`, which would lock the project
  to the older, less accurate diarizer and constrain the PyTorch version, in
  exchange for an alignment step this application does not need.

The trade-off accepted: the pyannote weights are gated, so a free Hugging Face
token is required once. Set `MT_DIARIZATION_MODEL` to swap in any other
pyannote-compatible pipeline.
