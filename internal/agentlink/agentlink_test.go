package agentlink

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersonjoe/trilha-spec/execution"
)

func TestFromClaudeHookMapsEveryEvent(t *testing.T) {
	cases := map[string]execution.AgentEvent{
		`{"hook_event_name":"SessionStart","session_id":"s1"}`:                   execution.EventSessionStart,
		`{"hook_event_name":"UserPromptSubmit"}`:                                 execution.EventPromptSubmit,
		`{"hook_event_name":"PreToolUse","tool_name":"Edit","session_id":"s1"}`:  execution.EventToolStart,
		`{"hook_event_name":"PostToolUse","tool_name":"Edit","session_id":"s1"}`: execution.EventToolEnd,
		`{"hook_event_name":"PermissionRequest"}`:                                execution.EventPermissionRequest,
		`{"hook_event_name":"Stop","stop_hook_active":false}`:                    execution.EventStop,
		`{"hook_event_name":"StopFailure","error":"rate_limit"}`:                 execution.EventStopFailure,
		`{"hook_event_name":"SessionEnd"}`:                                       execution.EventSessionEnd,
	}
	for payload, want := range cases {
		message, ok := FromClaudeHook([]byte(payload))
		if !ok || message.Type != TypeEvent || message.Event != want {
			t.Errorf("%s → %+v %v, want %s", payload, message, ok, want)
		}
	}
	if m, _ := FromClaudeHook([]byte(`{"hook_event_name":"PreToolUse","session_id":"s1"}`)); m.SessionID != "s1" {
		t.Errorf("session id lost: %+v", m)
	}
	if m, _ := FromClaudeHook([]byte(`{"hook_event_name":"StopFailure","error":"rate_limit"}`)); m.Text != "provider error: rate_limit" {
		t.Errorf("reason = %q", m.Text)
	}
	if m, _ := FromClaudeHook([]byte(`{"hook_event_name":"StopFailure","error":{"type":"billing_error"}}`)); m.Text != "provider error: billing_error" {
		t.Errorf("reason = %q", m.Text)
	}
	for _, payload := range []string{`not json`, `{"hook_event_name":"Notification"}`, `{}`} {
		if _, ok := FromClaudeHook([]byte(payload)); ok {
			t.Errorf("%s was accepted", payload)
		}
	}
}

// The socket carries messages from separate processes to the runner; a path
// too long for a Unix socket moves to a short private directory; a socket
// nobody listens on fails fast instead of hanging the agent.
func TestSocketDeliversMessagesAndFailsFast(t *testing.T) {
	deep := filepath.Join(t.TempDir(), strings.Repeat("d", 60), strings.Repeat("e", 60), "agent.sock")
	var mutex sync.Mutex
	var got []Message
	server, err := Listen(deep, func(m Message) {
		mutex.Lock()
		got = append(got, m)
		mutex.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(server.Path()) > maxSocketPath || server.Path() == deep {
		t.Fatalf("long path not moved: %s", server.Path())
	}
	if info, err := os.Stat(server.Path()); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket mode: %v %v", info, err)
	}
	for _, m := range []Message{{Type: TypeAsk, Text: "Which greeting?"}, {Type: TypeNote, Text: "uses UTF-8"}} {
		if err := Send(server.Path(), m, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	path := server.Path()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	if len(got) != 2 || got[0].Text != "Which greeting?" || got[1].Type != TypeNote {
		t.Fatalf("got %+v", got)
	}
	mutex.Unlock()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
	start := time.Now()
	if err := Send(path, Message{Type: TypeAsk, Text: "x"}, 500*time.Millisecond); err == nil {
		t.Fatal("a closed socket accepted a message")
	}
	if err := Send("", Message{Type: TypeAsk}, time.Second); err == nil || !strings.Contains(err.Error(), EnvSocket) {
		t.Fatalf("no socket: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("a dead socket took %s", time.Since(start))
	}
}
