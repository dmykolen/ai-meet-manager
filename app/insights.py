"""What an LLM adds on top of a transcript: a summary, chapters, and answers.

One Agno agent produces the whole structured summary in a single call; a second,
plain agent answers questions using passages retrieved from past meetings.
"""

from functools import lru_cache
from typing import TYPE_CHECKING

from pydantic import BaseModel, Field

if TYPE_CHECKING:
    from agno.agent import Agent

from app.config import settings

SUMMARY_RULES = """\
You are given the transcript of a workplace meeting: one line per speaker turn,
prefixed with its timestamp. Report only what was actually said — never invent
decisions, owners or dates. Attribute every action item to the speaker who
committed to it, using the names as they appear. Leave `owner` or `due` empty
when unstated. Chapters must cover the meeting in order, starting at 00:00, and
be coarse enough that each one is worth jumping to. Write in the language of the
transcript."""

ANSWER_RULES = """\
You answer questions about past meetings using only the passages you are given.
Each passage is labelled with its meeting and timestamp; cite those labels in
your answer. If the passages do not contain the answer, say so plainly."""


class ActionItem(BaseModel):
    task: str = Field(description="What was committed to, phrased as an instruction")
    owner: str = Field(default="", description="Who owns it")
    due: str = Field(default="", description="Deadline exactly as mentioned")


class Chapter(BaseModel):
    start: float = Field(description="Start of the chapter, in seconds")
    title: str = Field(description="A few words naming the topic")
    summary: str = Field(default="", description="One sentence on what was covered")


class Summary(BaseModel):
    overview: str = Field(description="Two or three sentences on what the meeting was about")
    chapters: list[Chapter] = Field(default_factory=list)
    topics: list[str] = Field(default_factory=list, description="Topics discussed")
    decisions: list[str] = Field(default_factory=list, description="Decisions actually reached")
    action_items: list[ActionItem] = Field(default_factory=list)
    open_questions: list[str] = Field(default_factory=list, description="Left unresolved")


def _model():
    from agno.models.anthropic import Claude  # deferred: ~5s of imports
    from agno.models.openai import OpenAIChat

    providers = {"openai": OpenAIChat, "anthropic": Claude}
    return providers[settings.llm_provider](id=settings.llm_model, api_key=settings.llm_api_key)


@lru_cache(maxsize=1)
def summariser() -> "Agent":
    from agno.agent import Agent

    return Agent(
        model=_model(),
        description="You summarise meeting transcripts for the people who attended them.",
        instructions=SUMMARY_RULES,
        output_schema=Summary,
        telemetry=False,
    )


@lru_cache(maxsize=1)
def assistant() -> "Agent":
    from agno.agent import Agent

    return Agent(
        model=_model(),
        description="You are the memory of a team's meetings.",
        instructions=ANSWER_RULES,
        telemetry=False,
    )


def clock(seconds: float) -> str:
    return f"{int(seconds) // 60:02d}:{int(seconds) % 60:02d}"


def as_text(result: dict) -> str:
    """The transcript as the models and the exports see it."""
    return "\n".join(
        f"[{clock(turn['start'])}] {turn.get('speaker', 'Speaker')}: {turn['text']}"
        for turn in result.get("segments", [])
        if turn.get("text")
    )


def summarise(result: dict) -> tuple[dict, int]:
    """A structured summary with chapters, plus the tokens it cost."""
    transcript = as_text(result)
    if not transcript:
        raise ValueError("This job has no transcript to summarise")
    run = summariser().run(transcript)
    return run.content.model_dump(), getattr(run.metrics, "total_tokens", 0) or 0


def answer(question: str, passages: list[str]) -> str:
    context = "\n\n".join(passages)
    return assistant().run(f"Passages:\n{context}\n\nQuestion: {question}").content
