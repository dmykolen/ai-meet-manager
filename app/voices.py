"""Voice signatures: enrolling people, recognising them, grouping a live stream."""

import logging
from collections.abc import Sequence

import numpy as np
from sqlmodel import Session, select

from app.config import settings
from app.media import SAMPLE_RATE
from app.models import Person

log = logging.getLogger(__name__)


def embed(audio: np.ndarray) -> list[float] | None:
    """One voice signature for a clip of a single person speaking."""
    if len(audio) < settings.voice_min_seconds * SAMPLE_RATE:
        return None
    import torch  # deferred: pulls in the whole ML stack, which the API never needs
    import torchaudio

    from app.engines import embedder

    model = embedder()
    waveform = torch.from_numpy(audio).reshape(1, 1, -1)
    rate = getattr(model, "sample_rate", SAMPLE_RATE)
    if rate != SAMPLE_RATE:
        waveform = torchaudio.functional.resample(waveform, SAMPLE_RATE, rate)
    if (where := getattr(model, "device", None)) is not None:
        waveform = waveform.to(where)  # the pipeline may be on a GPU or on Apple Silicon
    heard = model(waveform)
    vector = np.asarray(heard.cpu() if hasattr(heard, "cpu") else heard)[0]
    return None if not np.isfinite(vector).all() else vector.tolist()


def signature(audio: np.ndarray) -> list[float]:
    """The voiceprint to file under a person's name, or a ValueError saying why not.

    Enrolment is the one place worth being strict, because this sample is compared
    against every meeting from now on. Diarization answers both questions that matter
    — how much speech is really in the clip, and whether a second person is in it —
    and hands back the embedding it computed on the way, with overlapping speech
    already left out of it (`embedding_exclude_overlap` in the community-1 config).
    """
    import torch  # deferred: pulls in the whole ML stack, which the API never needs

    from app.engines import diarizer

    waveform = torch.from_numpy(audio).unsqueeze(0)  # pyannote wants (channel, time)
    output = diarizer()({"waveform": waveform, "sample_rate": SAMPLE_RATE})
    speech = output.speaker_diarization
    if len(speech.labels()) > 1:
        raise ValueError("There is more than one voice in that sample. Enrol one person speaking alone.")
    seconds = speech.get_timeline().support().duration()
    if seconds < settings.enrol_min_seconds:
        raise ValueError(f"Only {seconds:.1f}s of speech in that sample; {settings.enrol_min_seconds:.0f}s or more is needed.")
    return np.asarray(output.speaker_embeddings[0]).tolist()


def recognise(vectors: Sequence[Sequence[float]], people: Sequence[Person]) -> dict[int, str]:
    """Greedily pair voice signatures with enrolled people, best match first."""
    if not len(vectors) or not people:
        return {}
    scores = np.array([[_closeness(vector, person) for person in people] for vector in vectors])
    names, used_voices, used_people = {}, set(), set()
    for voice, person in sorted(np.ndindex(*scores.shape), key=lambda pair: -scores[pair]):
        if scores[voice, person] < settings.speaker_match_threshold:
            break
        if voice not in used_voices and person not in used_people:
            used_voices.add(voice)
            used_people.add(person)
            names[voice] = people[person].name
    return names


def _closeness(vector: Sequence[float], person: Person) -> float:
    """How well a signature matches the closest sample this person has left."""
    if not person.samples:
        return -1.0
    return float((_unit(np.array(person.samples)) @ _unit(np.array([vector])).T).max())


def _unit(matrix: np.ndarray) -> np.ndarray:
    return matrix / np.linalg.norm(matrix, axis=1, keepdims=True).clip(1e-9)


class Roll:
    """Who is speaking during one live stream.

    Every utterance is matched against the voices heard so far, so unknown
    speakers still get a stable label instead of no label at all.
    """

    def __init__(self, people: Sequence[Person] = ()) -> None:
        self.people = people
        self.centroids: list[np.ndarray] = []
        self.names: dict[int, str] = {}

    def label(self, audio: np.ndarray) -> str | None:
        vector = embed(audio)
        if vector is None:
            return None
        index = self._assign(np.array(vector, dtype=np.float32))
        return self.names.get(index, f"SPEAKER_{index:02d}")

    def _assign(self, vector: np.ndarray) -> int:
        if self.centroids:
            scores = _unit(np.array(self.centroids)) @ _unit(np.array([vector])).T
            best = int(scores.argmax())
            if scores[best] >= settings.speaker_match_threshold:
                self.centroids[best] = (self.centroids[best] + vector) / 2
                return best
        self.centroids.append(vector)
        index = len(self.centroids) - 1
        if known := recognise([vector], self.people):
            self.names[index] = known[0]
        return index


def remember(session: Session, name: str, vector: list[float], enrol: bool = False) -> None:
    """Keep another sample of a voice, so recognition improves with every meeting."""
    person = session.exec(select(Person).where(Person.name == name)).first()
    if person is None:
        if enrol:
            session.add(Person(name=name, samples=[vector]))
    else:
        person.samples = _kept([*person.samples, vector])
        session.add(person)


def _kept(samples: list[list[float]]) -> list[list[float]]:
    """Make room for one more voiceprint by dropping the one that teaches the least.

    Stopping at the tenth froze the roster: someone who changed headset was never
    learnt again, and whatever they recorded on their first day stayed for ever. The
    sample nearest another one is the redundant one, and a tie drops the older of the
    pair, so a fresh sample always displaces the stale twin rather than the other way.
    """
    if len(samples) <= settings.samples_per_person:
        return samples
    unit = _unit(np.array(samples))
    twins = unit @ unit.T
    np.fill_diagonal(twins, -1.0)
    return [sample for index, sample in enumerate(samples) if index != int(twins.max(axis=1).argmax())]


def everyone(session: Session) -> list[Person]:
    return list(session.exec(select(Person)))
