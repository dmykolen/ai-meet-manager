"""Meeting summaries, produced by an Agno agent over a finished transcript."""

from functools import lru_cache

from agno.agent import Agent
from agno.models.anthropic import Claude
from agno.models.openai import OpenAIChat
from pydantic import BaseModel, Field

from app.config import settings

INSTRUCTIONS = """\
You are given the transcript of a workplace meeting, one line per speaker turn.
Report only what was actually said: never invent decisions, owners or dates.
Attribute every action item to the speaker who committed to it, using the names
as they appear in the transcript. Leave `owner` or `due` empty when unstated.
Write in the language of the transcript."""


class ActionItem(BaseModel):
    task: str = Field(description="What was committed to, phrased as an instruction")
    owner: str = Field(default="", description="Who owns it")
    due: str = Field(default="", description="Deadline exactly as mentioned")


class Summary(BaseModel):
    overview: str = Field(description="Two or three sentences on what the meeting was about")
    topics: list[str] = Field(default_factory=list, description="Topics discussed")
    decisions: list[str] = Field(default_factory=list, description="Decisions actually reached")
    action_items: list[ActionItem] = Field(default_factory=list)
    open_questions: list[str] = Field(default_factory=list, description="Left unresolved")


@lru_cache(maxsize=1)
def agent() -> Agent:
    models = {"openai": OpenAIChat, "anthropic": Claude}
    model = models[settings.llm_provider](id=settings.llm_model, api_key=settings.llm_api_key)
    return Agent(
        model=model,
        description="You summarise meeting transcripts for the people who attended them.",
        instructions=INSTRUCTIONS,
        output_schema=Summary,
        telemetry=False,
    )


def summarise(result: dict) -> dict:
    """Turn a job result into a structured summary."""
    lines = [
        f"{turn.get('speaker', 'Speaker')}: {turn['text']}"
        for turn in result["segments"]
        if turn.get("text")
    ]
    if not lines:
        raise ValueError("This job has no transcript to summarise")
    return agent().run("\n".join(lines)).content.model_dump()
