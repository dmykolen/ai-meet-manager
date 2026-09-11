package service

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/home"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/insights"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/library"
	"github.com/dmykolen/meetings-transcript-and-diarize/standalone/internal/store"
)

func TestToolsExposeStoredInformation(t *testing.T) {
	meetings, recordingID, projectID := testMeetings(t)
	client, err := mcpclient.NewInProcessClient(NewMCP(meetings))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1"}
	if _, err := client.Initialize(t.Context(), init); err != nil {
		t.Fatal(err)
	}

	listed, err := client.ListTools(t.Context(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 9 {
		t.Fatalf("got %d tools, want 9", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint ||
			tool.Annotations.IdempotentHint == nil || !*tool.Annotations.IdempotentHint ||
			tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s has unsafe annotations: %+v", tool.Name, tool.Annotations)
		}
	}

	var recordings recordingsOutput
	decodeResult(t, call(t, client, "list_recordings", map[string]any{}), &recordings)
	if len(recordings.Recordings) != 1 || recordings.Recordings[0].ID != recordingID {
		t.Fatalf("unexpected recordings: %+v", recordings.Recordings)
	}

	var recording recordingOutput
	decodeResult(t, call(t, client, "get_recording", map[string]any{"id": recordingID}), &recording)
	if len(recording.Recording.Turns) != 2 || len(recording.Notes) != 1 ||
		recording.Analytics.Words == 0 {
		t.Fatalf("incomplete recording: %+v", recording)
	}

	var knowledge knowledgeSearchOutput
	decodeResult(t, call(t, client, "search_knowledge", map[string]any{"query": "launch"}), &knowledge)
	if len(knowledge.Hits) == 0 {
		t.Fatal("knowledge search did not find stored summary")
	}

	var project projectOutput
	decodeResult(t, call(t, client, "get_project", map[string]any{"id": projectID}), &project)
	if project.Project.ID != projectID || len(project.Recordings) != 1 || len(project.Notes) != 1 {
		t.Fatalf("incomplete project: %+v", project)
	}

	var people peopleOutput
	decodeResult(t, call(t, client, "list_people", map[string]any{}), &people)
	if len(people.People) != 1 || people.People[0].Name != "Alice" {
		t.Fatalf("unexpected people: %+v", people.People)
	}

	if result := call(t, client, "get_project", map[string]any{"id": int64(999)}); !result.IsError {
		t.Fatal("missing project should return a tool error")
	}
	if result := call(t, client, "list_recordings", map[string]any{
		"project_id": projectID, "include_deleted": true,
	}); !result.IsError {
		t.Fatal("conflicting list filters should return a tool error")
	}
}

func TestRunMCPServesStreamableHTTP(t *testing.T) {
	meetings, _, _ := testMeetings(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- RunMCP(ctx, meetings, addr) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCP server did not listen on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	client, err := mcpclient.NewStreamableHttpClient("http://" + addr + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "http-test", Version: "1"}
	if _, err := client.Initialize(t.Context(), init); err != nil {
		t.Fatal(err)
	}
	if tools, err := client.ListTools(t.Context(), mcp.ListToolsRequest{}); err != nil || len(tools.Tools) != 9 {
		t.Fatalf("HTTP tool discovery failed: tools=%v err=%v", tools, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("MCP server did not shut down")
	}
}

func testMeetings(t *testing.T) (*Meetings, int64, int64) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(home.Database(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	project, err := db.NewGroup("Launch")
	if err != nil {
		t.Fatal(err)
	}
	recording, err := db.Add(store.Recording{
		Kind: store.Meeting, Title: "Launch review", Audio: "launch.wav",
		Started: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Assign(recording.ID, project.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveTranscript(recording.ID, "en", 120, []store.Turn{
		{Start: 1, End: 4, Speaker: "Alice", Text: "The launch is ready."},
		{Start: 5, End: 8, Speaker: "Bob", Text: "Ship it tomorrow."},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSummary(recording.ID, &store.Summary{
		Title: "Launch review", Overview: "The launch is ready.",
		Decisions:   []string{"Launch tomorrow"},
		ActionItems: []store.Action{{Task: "Publish release", Owner: "Alice"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutNote(store.Sticky{Recording: recording.ID, Text: "Meeting note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutNote(store.Sticky{Project: project.ID, Text: "Project note"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Remember("Alice", []float32{1, 0}, store.Source{
		Recording: recording.ID, Speaker: "Alice",
	}); err != nil {
		t.Fatal(err)
	}

	cfg := home.Defaults()
	lib := library.New(db, nil, insights.New("", cfg.OpenAIModel, cfg.Language), home.Recordings(dir))
	return New(db, lib, dir, cfg), recording.ID, project.ID
}

func call(t *testing.T, client *mcpclient.Client, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = arguments
	result, err := client.CallTool(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decodeResult(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool returned an error: %+v", result.Content)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
