# MCP access

The desktop app serves its stored meetings over Streamable HTTP while it is
running:

```text
http://127.0.0.1:8765/mcp
```

The endpoint is loopback-only and every tool is read-only. It exposes
transcripts, summaries, analytics, notes, projects, action items, briefings,
knowledge search, and recognised people. It never returns the OpenAI key or raw
voiceprint vectors.

Connect Claude Code:

```bash
claude mcp add --transport http meeting-transcriber http://127.0.0.1:8765/mcp
```

Connect Codex by adding this to `~/.codex/config.toml`:

```toml
[mcp_servers.meeting-transcriber]
url = "http://127.0.0.1:8765/mcp"
```

The ChatGPT desktop app and Codex IDE extension can use the same URL through
**Settings → MCP servers → Add server → Streamable HTTP**.

Set `MT_MCP_ADDR` before starting the app to use another loopback address, for
example `MT_MCP_ADDR=127.0.0.1:9876`. Non-loopback addresses are rejected so
the meeting archive cannot be exposed to the network accidentally.
