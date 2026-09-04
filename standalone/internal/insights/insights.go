// Package insights is everything the app asks a language model to do: the
// summary of a meeting, and questions asked across all of them.
//
// It uses the official Go SDK against the Responses API. OpenAI's Agents SDK
// ships only for Python and TypeScript; the Go port is at v0.1.0 and has not
// moved in months, and what is needed here is two prompts with a fixed schema
// rather than tools, handoffs or guardrails. Everything below goes through Ask
// and Structured, so the day this does grow an agent loop, no caller changes.
//
// This is also the only part of the app that sends anything anywhere. Audio,
// transcripts and voices never leave the machine; a summary sends the text of
// one meeting, and a question sends the passages that match it.
package insights

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// ErrNoKey is what every function here returns when there is no API key, so
// that the app can disable these features rather than fail a recording.
var ErrNoKey = errors.New("no OpenAI key: summaries and questions are off, " +
	"and transcription is unaffected")

// Client talks to the model. The zero value is unusable; use New.
type Client struct {
	api      openai.Client
	model    string
	language string
	ready    bool
}

// Tongue turns a language code into something to put in a prompt. Written out
// rather than passed as "uk", because a model told to answer in "uk" sometimes
// decides that is a country.
var Tongue = map[string]string{
	"uk": "Ukrainian", "en": "English", "pl": "Polish", "de": "German",
	"fr": "French", "es": "Spanish", "it": "Italian", "cs": "Czech",
	"nl": "Dutch", "pt": "Portuguese", "ro": "Romanian", "tr": "Turkish",
}

// New takes the language the summaries and answers must be written in. Empty
// means the language of the meeting, whatever that turns out to be.
func New(key, model, language string) *Client {
	if key == "" {
		return &Client{}
	}
	if model == "" {
		model = "gpt-5.4-mini"
	}
	tongue := "the language the meeting was held in"
	if named, known := Tongue[strings.ToLower(language)]; known {
		tongue = named
	}
	return &Client{
		api:      openai.NewClient(option.WithAPIKey(key)),
		model:    model,
		language: tongue,
		ready:    true,
	}
}

// Ready reports whether these features are available at all, which the UI shows
// rather than discovering on a click.
func (c *Client) Ready() bool { return c != nil && c.ready }

// ActionItem is something somebody committed to.
type ActionItem struct {
	Task  string `json:"task"`
	Owner string `json:"owner"`
	Due   string `json:"due"`
}

// Chapter is a stretch of the meeting on one topic, with the timestamp that
// makes it clickable.
type Chapter struct {
	Start   float64 `json:"start"`
	Title   string  `json:"title"`
	Summary string  `json:"summary"`
}

// Summary is what a person wants instead of the transcript.
type Summary struct {
	Title         string       `json:"title"`
	Overview      string       `json:"overview"`
	Chapters      []Chapter    `json:"chapters"`
	Topics        []string     `json:"topics"`
	Decisions     []string     `json:"decisions"`
	ActionItems   []ActionItem `json:"action_items"`
	OpenQuestions []string     `json:"open_questions"`
}

// Turn is the shape this package needs from a transcript, so that it does not
// depend on the store or the engine.
type Turn struct {
	Start   float64
	Speaker string
	Text    string
}

const summaryPrompt = `You are given the transcript of one meeting, with timestamps and speakers.

Write it up for somebody who was not there and will not read the transcript.
- title: four to eight words naming what this meeting was actually about. Not "Team Meeting".
- overview: two or three sentences. What was this about, and what came of it.
- topics: the handful of subjects covered, a few words each.
- decisions: only things actually settled. If nothing was decided, say nothing.
- action_items: only things somebody committed to. Attribute each one to the
  speaker who committed to it, using the name exactly as the transcript spells
  it. Leave the owner or the deadline empty rather than inventing either, and
  copy a deadline in the words it was said in.
- chapters: cover the meeting in order, starting at 00:00, coarse enough that
  each one is worth jumping to — a handful for an hour, not one per minute.
- open_questions: raised and left unresolved. Phrase each as the question it
  was, so that the same question asked again next week is recognisable.

Transcripts of real meetings are imperfect and words are sometimes misheard.
Where a passage is garbled, leave it out rather than guessing what it meant.

Write in %s. This holds even when the transcript itself contains other
languages, borrowed words or whole sentences in another language — those are
what people say, and they do not change what you answer in.`

// Summarise turns a transcript into the thing people actually read.
func (c *Client) Summarise(ctx context.Context, turns []Turn) (*Summary, error) {
	if !c.Ready() {
		return nil, ErrNoKey
	}
	var out Summary
	if err := c.Structured(ctx, fmt.Sprintf(summaryPrompt, c.language), Transcript(turns), "meeting_summary", schema, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

const askPrompt = `Answer the question from these passages of past meetings, and
from nothing else. Cite the meeting and the time for anything you assert. If the
passages do not answer it, say so plainly rather than reasoning around it.
Answer in the language of the question.`

// Answer replies to a question using passages already found by search.
func (c *Client) Answer(ctx context.Context, question string, passages []string) (string, error) {
	if !c.Ready() {
		return "", ErrNoKey
	}
	if len(passages) == 0 {
		return "", errors.New("nothing in the transcripts covers that")
	}
	body := fmt.Sprintf("Question: %s\n\nPassages:\n%s", question, strings.Join(passages, "\n\n"))
	return c.Ask(ctx, askPrompt, body)
}

// Ask is one prompt in, text out. Exported because it is the seam: an agent
// loop, when one is needed, replaces the inside of this and nothing else.
func (c *Client) Ask(ctx context.Context, instructions, input string) (string, error) {
	if !c.Ready() {
		return "", ErrNoKey
	}
	resp, err := c.api.Responses.New(ctx, responses.ResponseNewParams{
		Model:        c.model,
		Instructions: openai.String(instructions),
		Input:        responses.ResponseNewParamsInputUnion{OfString: openai.String(input)},
	})
	if err != nil {
		return "", err
	}
	return resp.OutputText(), nil
}

// Structured is Ask with a schema the model must obey, decoded into out.
func (c *Client) Structured(ctx context.Context, instructions, input, name string, jsonSchema map[string]any, out any) error {
	if !c.Ready() {
		return ErrNoKey
	}
	resp, err := c.api.Responses.New(ctx, responses.ResponseNewParams{
		Model:        c.model,
		Instructions: openai.String(instructions),
		Input:        responses.ResponseNewParamsInputUnion{OfString: openai.String(input)},
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:   name,
					Schema: jsonSchema,
					Strict: openai.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(resp.OutputText()), out)
}

// Transcript renders turns the way the model reads them best: one line each,
// with the time and who said it.
func Transcript(turns []Turn) string {
	var b strings.Builder
	for _, t := range turns {
		fmt.Fprintf(&b, "[%s] %s: %s\n", clock(t.Start), or(t.Speaker, "Unknown"), t.Text)
	}
	return b.String()
}

func clock(seconds float64) string {
	s := int(seconds)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
