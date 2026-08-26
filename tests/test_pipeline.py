import math

import numpy as np
import pytest
from pyannote.core import Annotation, Segment

from app.models import Person
from app.pipeline import (
    SENTENCE_SPAN,
    _analytics,
    _confidence,
    _hallucinated,
    _turns,
    analytics_of,
)
from app.voices import recognise


class Word:
    def __init__(self, text: str, start: float, end: float):
        self.word, self.start, self.end = text, start, end


class Chunk:
    """A faster-whisper segment, as far as this code is concerned."""

    def __init__(self, *words: Word, no_speech: float = 0.1, logprob: float = -0.2, text=None):
        self.words = words
        self.start = words[0].start if words else 0.0
        self.end = words[-1].end if words else 0.0
        self.text = text if text is not None else "".join(w.word for w in words)
        self.no_speech_prob, self.avg_logprob = no_speech, logprob


def annotation() -> Annotation:
    speech = Annotation()
    speech[Segment(0, 2)] = "SPEAKER_00"
    speech[Segment(2, 4)] = "SPEAKER_01"
    return speech


def same(seconds: float) -> float:
    return seconds


# --- sentences ----------------------------------------------------------------


def test_a_batched_block_is_split_back_onto_its_pauses():
    """Batched decoding returns one 30-second block; the reader needs sentences."""
    block = Chunk(
        Word(" Ми", 0.0, 0.3),
        Word(" домовились.", 0.3, 1.0),
        Word(" Далі", 3.0, 3.4),
        Word(" міграція.", 3.4, 4.1),  # after a clear pause
    )
    turns = _turns([block], same)
    assert [t["text"] for t in turns] == ["Ми домовились.", "Далі міграція."]
    assert turns[1]["start"] == 3.0, "the second sentence starts where its first word does"


def test_a_pause_in_the_middle_of_a_sentence_is_not_a_break():
    """Splitting on silence alone orphaned tails like "in the MVP." onto their own row."""
    block = Chunk(
        Word(" Це", 0.0, 0.3),
        Word(" потрапляє", 0.3, 1.0),
        Word(" в", 1.6, 1.7),
        Word(" MVP.", 1.7, 2.2),  # half a second of thinking
    )
    assert [t["text"] for t in _turns([block], same)] == ["Це потрапляє в MVP."]


def test_a_speaker_who_never_pauses_is_still_broken_up():
    words = [Word(f" сло{i}", i * 0.4, i * 0.4 + 0.35) for i in range(80)]
    turns = _turns([Chunk(*words)], same)
    assert len(turns) > 1
    assert max(t["end"] - t["start"] for t in turns) < SENTENCE_SPAN


def test_unbatched_segments_are_left_exactly_as_whisper_split_them():
    plain = Chunk(text="Одне речення.")
    plain.words = None
    plain.start, plain.end = 1.0, 2.5
    assert _turns([plain], same) == [{"start": 1.0, "end": 2.5, "text": "Одне речення.", "confidence": _confidence(plain)}]


def test_sentence_timestamps_are_mapped_back_onto_the_original_recording():
    block = Chunk(Word(" Слово", 1.0, 1.4))
    assert _turns([block], lambda seconds: seconds + 60)[0]["start"] == 61.0


# --- turns --------------------------------------------------------------------


def test_consecutive_words_of_one_speaker_become_a_single_turn():
    chunks = [
        Chunk(Word(" Hello", 0.0, 0.5), Word(" there", 0.5, 1.0)),
        Chunk(Word(" Hi", 2.1, 2.5), Word(" back", 2.5, 3.0)),
    ]
    assert _turns(chunks, same, annotation()) == [
        {
            "start": 0.0,
            "end": 1.0,
            "speaker": "SPEAKER_00",
            "text": "Hello there",
            "confidence": 0.819,
        },
        {"start": 2.1, "end": 3.0, "speaker": "SPEAKER_01", "text": "Hi back", "confidence": 0.819},
    ]


def test_turn_timestamps_are_mapped_back_onto_the_original_recording():
    turns = _turns([Chunk(Word(" Hi", 0.0, 1.0))], lambda t: t + 30, annotation())
    assert (turns[0]["start"], turns[0]["end"]) == (30.0, 31.0)


def test_words_outside_any_speech_turn_keep_the_previous_speaker():
    chunk = Chunk(Word(" Hello", 0.1, 0.4), Word(" ok", 9.0, 9.2))
    turns = _turns([chunk], same, annotation())
    assert [t["speaker"] for t in turns] == ["SPEAKER_00", "SPEAKER_00"]
    assert [t["text"] for t in turns] == ["Hello", "ok"], "nine seconds apart is not one turn"


def test_one_speaker_holding_the_floor_is_still_broken_into_rows():
    """Diarization will hand back three unbroken minutes of one person."""
    monologue = Annotation()
    monologue[Segment(0, 60)] = "SPEAKER_00"
    words = [Word(f" сло{i}", i * 0.4, i * 0.4 + 0.35) for i in range(120)]
    turns = _turns([Chunk(*words)], same, monologue)
    assert {t["speaker"] for t in turns} == {"SPEAKER_00"}
    assert len(turns) > 1, "a speaker change is not the only reason to start a row"
    assert max(t["end"] - t["start"] for t in turns) < SENTENCE_SPAN


def test_no_words_yields_no_turns():
    assert _turns([Chunk()], same, annotation()) == []


# --- confidence and hallucinations --------------------------------------------


def test_confidence_is_the_probability_behind_the_log_probability():
    assert _confidence(Chunk(Word(" x", 0, 1), logprob=math.log(0.5))) == 0.5


def test_text_invented_over_silence_is_dropped():
    assert _hallucinated(Chunk(Word(" Thanks for watching", 0, 2), no_speech=0.95, logprob=-1.4))


@pytest.mark.parametrize(
    "chunk",
    [
        Chunk(Word(" real speech", 0, 2), no_speech=0.95, logprob=-0.2),  # confident enough
        Chunk(Word(" real speech", 0, 2), no_speech=0.2, logprob=-1.4),  # speech was detected
    ],
)
def test_uncertain_but_plausible_text_is_kept(chunk):
    assert not _hallucinated(chunk)


def test_empty_text_is_dropped():
    assert _hallucinated(Chunk(Word("  ", 0, 1), text="   "))


# --- talk time ----------------------------------------------------------------


def test_analytics_report_who_held_the_floor():
    speech = Annotation()
    speech[Segment(0, 30)] = "Dima"
    speech[Segment(28, 40)] = "Olena"
    speech[Segment(50, 55)] = "Dima"

    report = _analytics(speech, duration=60)
    assert report["speech_seconds"] == 45.0  # 0-40 and 50-55
    assert report["silence_seconds"] == 15.0
    assert report["overlap_seconds"] == 2.0
    dima, olena = report["speakers"]
    assert (dima["speaker"], dima["seconds"], dima["turns"]) == ("Dima", 35.0, 2)
    assert dima["longest_turn"] == 30.0
    assert olena["share"] == pytest.approx(12 / 47, abs=0.01)


def test_analytics_can_be_rebuilt_from_a_live_transcript():
    turns = [
        {"start": 0, "end": 10, "speaker": "Dima", "text": "hello"},
        {"start": 10, "end": 15, "speaker": "Olena", "text": "hi"},
    ]
    report = analytics_of(turns, duration=20)
    assert report["silence_seconds"] == 5.0
    assert [row["speaker"] for row in report["speakers"]] == ["Dima", "Olena"]


# --- voice identification -----------------------------------------------------

DIMA = [1.0, 0.0, 0.0]
OLENA = [0.0, 1.0, 0.0]


def people() -> list[Person]:
    return [Person(name="Dima", samples=[DIMA]), Person(name="Olena", samples=[OLENA])]


def test_speakers_are_named_after_the_voices_they_match():
    assert recognise(np.array([OLENA, DIMA]), people()) == {0: "Olena", 1: "Dima"}


def test_unknown_voices_are_left_alone():
    assert recognise(np.array([[0.0, 0.0, 1.0]]), people()) == {}


def test_one_person_is_never_assigned_to_two_speakers():
    voices = np.array([[1.0, 0.1, 0.0], [1.0, 0.0, 0.0]])
    assert recognise(voices, people()) == {1: "Dima"}


def test_any_of_a_persons_samples_can_match():
    person = Person(name="Dima", samples=[[0.0, 0.0, 1.0], DIMA])
    assert recognise(np.array([DIMA]), [person]) == {0: "Dima"}


@pytest.mark.parametrize("vectors,known", [([], True), ([DIMA], False)])
def test_recognition_is_skipped_without_voices_or_people(vectors, known):
    assert recognise(np.array(vectors), people() if known else []) == {}
