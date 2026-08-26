"""Transcription and diarization over a decoded, silence-free waveform.

Both models are handed the same trimmed audio and every timestamp they return is
mapped back onto the original recording, so the transcript lines up with what a
listener hears while the models never spend time on silence.
"""

import logging
import math
import time
from collections.abc import Callable, Sequence

import numpy as np
import torch
from pyannote.audio.pipelines.speaker_diarization import DiarizeOutput
from pyannote.core import Annotation, Segment

from app import media, voices
from app.config import settings
from app.engines import asr, backend, batching, diarizer, live_asr, torch_device
from app.media import SAMPLE_RATE, Remap
from app.models import Options, Person, Task

TRANSCRIPTION_SHARE = 0.7  # of the progress bar when diarization follows
DIARIZATION_STEPS = 4
SENTENCE_GAP = 0.3  # a pause this long ends a sentence, if it sounded finished
SENTENCE_BREAK = 1.0  # this long ends one regardless
SENTENCE_SPAN = 14.0  # and nothing runs longer than this, pause or not
SENTENCE_ENDINGS = (".", "?", "!", "…", ":")
log = logging.getLogger(__name__)

Progress = Callable[[float], None]


def transcribe(
    audio: np.ndarray,
    vocabulary: str | None = None,
    word_timestamps: bool = False,
    report: Callable[[float], None] | None = None,
    *,
    language: str | None = None,
    live: bool = False,
) -> tuple[list, str]:
    """Whisper over a waveform. `report` receives the seconds transcribed so far.

    Naming the `language` skips detection, which costs a whole extra encoder pass —
    measured at ~2s per call, which is most of the latency of a live utterance. `live`
    picks the model meant for one short utterance at a time, where batching is a wash.
    """
    options = {
        "language": language or settings.language,
        "hotwords": vocabulary or settings.vocabulary or None,
        "word_timestamps": word_timestamps,
        "vad_filter": not settings.trim_silence,  # already trimmed, unless disabled
    }
    if live:
        # Greedy decoding is 1.8x faster on a CPU and, measured against this meeting, no
        # worse; and one utterance is not a continuation of the last, so conditioning on
        # the previous text only invites the model to invent a transition.
        options |= {"beam_size": 1, "condition_on_previous_text": False}
    elif batching():
        # The batched pipeline slices the audio on its own VAD and refuses to run without
        # it. Timestamps come back relative to what it was handed, which `remap` expects.
        options |= {"batch_size": settings.batch_size, "vad_filter": True}
    segments, info = (live_asr() if live else asr()).transcribe(audio, **options)

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
    started = time.monotonic()
    original = media.decode(path)
    duration = len(original) / SAMPLE_RATE
    log.info("Decoded %.1f min in %.1fs", duration / 60, time.monotonic() - started)
    audio, remap = media.speech_only(original)
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
        result["segments"] = [{"start": round(remap(turn.start), 3), "end": round(remap(turn.end), 3), "speaker": who} for turn, _, who in speech.itertracks(yield_label=True)]
        result["speakers"] = sorted(speech.labels())
        result["analytics"] = _analytics(speech, duration)
        return result, voiceprints

    with_speakers = task is Task.transcribe_diarize
    share = TRANSCRIPTION_SHARE if with_speakers else 1.0
    log.info("Transcribing %.1f min of speech with %s", heard / 60, backend())
    spoke = time.monotonic()
    segments, result["language"] = transcribe(
        audio,
        options.vocabulary,
        # Batched decoding is 2x faster and hands back 30-second blocks; the words it
        # throws in for free are what turns those back into sentences.
        word_timestamps=with_speakers or batching(),
        report=lambda seconds: report(share * min(seconds / max(heard, 1e-9), 1.0)),
    )

    log.info(
        "Transcribed in %s, %d segments, language %s",
        _pace(time.monotonic() - spoke, heard),
        len(segments),
        result["language"],
    )

    if not with_speakers:
        result["segments"] = _turns(segments, remap)
        return result, {}

    log.info("Diarizing %.1f min on %s", heard / 60, torch_device())
    split = time.monotonic()
    speech, exclusive, voiceprints = _diarize(audio, options, people, lambda done: report(share + (1 - share) * done))
    result["segments"] = _turns(segments, remap, exclusive)
    result["speakers"] = sorted(speech.labels())
    result["analytics"] = _analytics(speech, duration)
    log.info(
        "Diarized in %s, %d speakers, %d rows",
        _pace(time.monotonic() - split, heard),
        len(result["speakers"]),
        len(result["segments"]),
    )
    return result, voiceprints


def _pace(took: float, seconds: float) -> str:
    """Both numbers, because "4 minutes" means nothing without the length of the audio."""
    return f"{took:.1f}s ({seconds / took:.1f}x realtime)" if took > 0 else f"{took:.1f}s"


def _turns(segments, remap: Remap, speech: Annotation | None = None) -> list[dict]:
    """Words into rows a person can read.

    A row ends when the speaker changes, when the sentence sounded finished and a pause
    followed, or when it has simply run on too long. None of those boundaries works
    alone: batched decoding hands back 30-second blocks, and diarization happily hands
    back three unbroken minutes of one person holding the floor.
    """
    runs: list[list] = []  # start, end, text, confidence, speaker
    speaker = "SPEAKER_00" if speech is not None else None
    for chunk in segments:
        confidence = _confidence(chunk)
        if not chunk.words:  # unbatched decoding already breaks on sentences
            runs.append([chunk.start, chunk.end, chunk.text, confidence, speaker])
            continue
        for word in chunk.words:
            if speech is not None:
                speaker = speech.argmax(Segment(word.start, word.end)) or speaker
            if runs and runs[-1][4] == speaker and _continues(runs[-1], word):
                runs[-1][1], runs[-1][2] = word.end, runs[-1][2] + word.word
            else:
                runs.append([word.start, word.end, word.word, confidence, speaker])
    return [
        {
            "start": round(remap(start), 3),
            "end": round(remap(end), 3),
            **({"speaker": who} if who is not None else {}),
            "text": text.strip(),
            "confidence": confidence,
        }
        for start, end, text, confidence, who in runs
        if text.strip()
    ]


def _continues(run: list, word) -> bool:
    """People pause in the middle of a sentence, so a pause alone is not a break.

    Splitting on silence alone left 12% of the rows as orphaned tails like "in the MVP.";
    asking that the run also sound finished halves that.
    """
    if word.end - run[0] >= SENTENCE_SPAN:
        return False
    pause = word.start - run[1]
    if pause >= SENTENCE_BREAK:
        return False
    return pause < SENTENCE_GAP or not run[2].rstrip().endswith(SENTENCE_ENDINGS)


def _hallucinated(segment) -> bool:
    """Whisper writes plausible sentences over silence; these are the tells."""
    return (segment.no_speech_prob > settings.drop_no_speech_above and segment.avg_logprob < settings.drop_logprob_below) or not segment.text.strip()


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
    output: DiarizeOutput = diarizer()({"waveform": waveform, "sample_rate": SAMPLE_RATE}, hook=hook, **options.speakers())
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


def analytics_of(segments: list[dict], duration: float) -> dict:
    """The same talk-time figures, for transcripts that were built turn by turn."""
    speech = Annotation()
    for index, turn in enumerate(segments):
        if turn.get("speaker") and turn["end"] > turn["start"]:
            speech[Segment(turn["start"], turn["end"]), index] = turn["speaker"]
    return _analytics(speech, duration)
