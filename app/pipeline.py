"""Transcription and diarization over a decoded, silence-free waveform.

Both models are handed the same trimmed audio and every timestamp they return is
mapped back onto the original recording, so the transcript lines up with what a
listener hears while the models never spend time on silence.
"""

import logging
import math
from collections.abc import Callable, Sequence

import numpy as np
import torch
from pyannote.audio.pipelines.speaker_diarization import DiarizeOutput
from pyannote.core import Annotation, Segment

from app import media, voices
from app.config import settings
from app.engines import asr, batching, diarizer
from app.media import SAMPLE_RATE, Remap
from app.models import Options, Person, Task

TRANSCRIPTION_SHARE = 0.7  # of the progress bar when diarization follows
DIARIZATION_STEPS = 4
log = logging.getLogger(__name__)

Progress = Callable[[float], None]


def transcribe(
    audio: np.ndarray,
    vocabulary: str | None = None,
    word_timestamps: bool = False,
    report: Callable[[float], None] | None = None,
) -> tuple[list, str]:
    """Whisper over a waveform. `report` receives the seconds transcribed so far."""
    options = {
        "language": settings.language,
        "hotwords": vocabulary or settings.vocabulary or None,
        "word_timestamps": word_timestamps,
        "vad_filter": not settings.trim_silence,  # already trimmed, unless disabled
    }
    if batching():
        options["batch_size"] = settings.batch_size
    segments, info = asr().transcribe(audio, **options)

    collected = []
    for segment in segments:  # iterating the generator is what runs inference
        if _hallucinated(segment):
            log.debug("Dropping %r (no_speech=%.2f)", segment.text, segment.no_speech_prob)
            continue
        collected.append(segment)
        if report:
            report(segment.end)
    return collected, info.language


def run(
    path,
    task: Task,
    options: Options,
    people: Sequence[Person] = (),
    report: Progress = lambda _: None,
) -> tuple[dict, dict]:
    """Transcribe and/or diarize one recording. Blocking and CPU/GPU bound.

    Returns the result and one voice signature per speaker, which lets a later
    correction of the labels teach the roster who that voice belongs to.
    """
    original = media.decode(path)
    audio, remap = media.speech_only(original)
    duration = len(original) / SAMPLE_RATE
    heard = len(audio) / SAMPLE_RATE
    result: dict = {
        "duration": round(duration, 3),
        "speech_duration": round(heard, 3),
        "language": None,
        "speakers": [],
        "segments": [],
    }

    if task is Task.diarize:
        speech, _, voiceprints = _diarize(audio, options, people, report)
        result["segments"] = [
            {"start": round(remap(turn.start), 3), "end": round(remap(turn.end), 3), "speaker": who}
            for turn, _, who in speech.itertracks(yield_label=True)
        ]
        result["speakers"] = sorted(speech.labels())
        result["analytics"] = _analytics(speech, duration)
        return result, voiceprints

    with_speakers = task is Task.transcribe_diarize
    share = TRANSCRIPTION_SHARE if with_speakers else 1.0
    segments, result["language"] = transcribe(
        audio,
        options.vocabulary,
        word_timestamps=with_speakers,
        report=lambda seconds: report(share * min(seconds / max(heard, 1e-9), 1.0)),
    )

    if not with_speakers:
        result["segments"] = [
            {
                "start": round(remap(s.start), 3),
                "end": round(remap(s.end), 3),
                "text": s.text.strip(),
                "confidence": _confidence(s),
            }
            for s in segments
        ]
        return result, {}

    speech, exclusive, voiceprints = _diarize(
        audio, options, people, lambda done: report(share + (1 - share) * done)
    )
    result["segments"] = _speaker_turns(segments, exclusive, remap)
    result["speakers"] = sorted(speech.labels())
    result["analytics"] = _analytics(speech, duration)
    return result, voiceprints


def _hallucinated(segment) -> bool:
    """Whisper writes plausible sentences over silence; these are the tells."""
    return (
        segment.no_speech_prob > settings.drop_no_speech_above
        and segment.avg_logprob < settings.drop_logprob_below
    ) or not segment.text.strip()


def _confidence(segment) -> float:
    return round(math.exp(segment.avg_logprob), 3)


def _diarize(
    audio: np.ndarray,
    options: Options,
    people: Sequence[Person],
    report: Progress = lambda _: None,
) -> tuple[Annotation, Annotation, dict]:
    """Returns the diarization with overlaps, the exclusive one, and the voiceprints."""
    steps: list[str] = []

    def hook(step_name, _artifact, file=None, total=None, completed=None) -> None:
        if step_name not in steps:
            steps.append(step_name)
        within = (completed or 0) / (total or 1)
        report(min((len(steps) - 1 + within) / DIARIZATION_STEPS, 1.0))

    waveform = torch.from_numpy(audio).unsqueeze(0)  # pyannote wants (channel, time)
    output: DiarizeOutput = diarizer()(
        {"waveform": waveform, "sample_rate": SAMPLE_RATE}, hook=hook, **options.speakers()
    )
    # The exclusive variant drops overlapping speech and is meant for alignment;
    # the plain one keeps the overlaps that make talk-time analytics honest.
    speech, exclusive = output.speaker_diarization, output.exclusive_speaker_diarization
    labels = speech.labels()
    embeddings = [] if output.speaker_embeddings is None else output.speaker_embeddings

    names = voices.recognise(embeddings, people)
    if mapping := {labels[index]: name for index, name in names.items()}:
        speech, exclusive = speech.rename_labels(mapping), exclusive.rename_labels(mapping)
        labels = [mapping.get(label, label) for label in labels]

    prints = zip(labels, embeddings, strict=False)
    return speech, exclusive, {label: np.asarray(v).tolist() for label, v in prints}


def _analytics(speech: Annotation, duration: float) -> dict:
    """Who held the floor, and for how long."""
    chart = dict(speech.chart())
    spoken = sum(chart.values())
    covered = speech.get_timeline().support().duration()
    return {
        "speech_seconds": round(covered, 1),
        "silence_seconds": round(max(duration - covered, 0), 1),
        "overlap_seconds": round(max(spoken - covered, 0), 1),
        "speakers": [
            {
                "speaker": label,
                "seconds": round(seconds, 1),
                "share": round(seconds / spoken, 3) if spoken else 0.0,
                "turns": len(speech.label_timeline(label)),
                "longest_turn": round(max(s.duration for s in speech.label_timeline(label)), 1),
            }
            for label, seconds in chart.items()
        ],
    }


def _speaker_turns(segments, speech: Annotation, remap: Remap) -> list[dict]:
    """Label each word with the speaker talking over it, then merge consecutive words."""
    turns: list[dict] = []
    speaker = "SPEAKER_00"
    for chunk in segments:
        for word in chunk.words or []:
            speaker = speech.argmax(Segment(word.start, word.end)) or speaker
            if turns and turns[-1]["speaker"] == speaker:
                turns[-1]["end"] = round(remap(word.end), 3)
                turns[-1]["text"] += word.word
            else:
                turns.append(
                    {
                        "start": round(remap(word.start), 3),
                        "end": round(remap(word.end), 3),
                        "speaker": speaker,
                        "text": word.word,
                        "confidence": _confidence(chunk),
                    }
                )
    for turn in turns:
        turn["text"] = turn["text"].strip()
    return turns


def analytics_of(segments: list[dict], duration: float) -> dict:
    """The same talk-time figures, for transcripts that were built turn by turn."""
    speech = Annotation()
    for index, turn in enumerate(segments):
        if turn.get("speaker") and turn["end"] > turn["start"]:
            speech[Segment(turn["start"], turn["end"]), index] = turn["speaker"]
    return _analytics(speech, duration)
