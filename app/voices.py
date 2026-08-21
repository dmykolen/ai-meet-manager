"""Voice signatures: enrolling people, recognising them, grouping a live stream."""

import logging
from collections.abc import Sequence

import numpy as np
from sqlmodel import Session, select

from app.config import settings
from app.media import SAMPLE_RATE
from app.models import Person

MIN_SAMPLES = SAMPLE_RATE  # a second of speech is the least worth embedding
SAMPLES_PER_PERSON = 10  # enough to cover different microphones and moods
log = logging.getLogger(__name__)


def embed(audio: np.ndarray) -> list[float] | None:
    """One voice signature for a clip of a single person speaking."""
    if len(audio) < MIN_SAMPLES:
        return None
    import torch  # deferred: pulls in the whole ML stack, which the API never needs
    import torchaudio

    from app.engines import embedder

    model = embedder()
    waveform = torch.from_numpy(audio).reshape(1, 1, -1)
    rate = getattr(model, "sample_rate", SAMPLE_RATE)
    if rate != SAMPLE_RATE:
        waveform = torchaudio.functional.resample(waveform, SAMPLE_RATE, rate)
    vector = np.asarray(model(waveform))[0]
    return None if not np.isfinite(vector).all() else vector.tolist()


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
    elif len(person.samples) < SAMPLES_PER_PERSON:
        person.samples = [*person.samples, vector]
        session.add(person)


def everyone(session: Session) -> list[Person]:
    return list(session.exec(select(Person)))
