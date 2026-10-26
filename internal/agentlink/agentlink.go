// Package agentlink is the channel between an agent and the runner that
// started it. The runner listens on a Unix socket for the duration of a run
// and exports its path as TRILHA_RUN_SOCKET; `trilha-runner hook`, `ask`,
// `note`, `overview` and `attention`, run by the agent, each send one JSON
// message and exit. Delivery is best effort and bounded in time: an agent is
// never held up, or failed, because the runner is not listening.
package agentlink

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emersonjoe/trilha-spec/execution"
)

// Message types.
const (
	TypeEvent     = "event"
	TypeAsk       = "ask"
	TypeAttention = "attention"
	TypeNote      = "note"
	TypeOverview  = "overview"
)

// Message is one thing the agent tells the runner.
type Message struct {
	Type      string               `json:"type"`
	Event     execution.AgentEvent `json:"event,omitempty"`
	Text      string               `json:"text,omitempty"`
	Clear     bool                 `json:"clear,omitempty"`
	SessionID string               `json:"session_id,omitempty"`
}

// MaxMessage bounds one message; MaxMessages bounds a run, so a looping agent
// cannot fill the runner's memory.
const (
	MaxMessage  = 64 << 10
	MaxMessages = 10000
)

// Environment names the runner exports to the agent.
const (
	EnvSocket = "TRILHA_RUN_SOCKET"
	EnvRunID  = "TRILHA_RUN_ID"
)

// maxSocketPath is below the smallest sun_path of the platforms the runner
// ships for (104 bytes on macOS, 108 on Linux).
const maxSocketPath = 100

// Send delivers one message within timeout. It answers an error the caller
// may print but never needs to act on.
func Send(path string, message Message, timeout time.Duration) error {
	if path == "" {
		return errors.New("agentlink: " + EnvSocket + " is not set: not inside a trilha run")
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > MaxMessage {
		return errors.New("agentlink: message too large")
	}
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return err
	}
	// The runner answers once it has handled the message, so a message whose
	// Send returned is one the runner has: a run that ends right after an ask
	// still sees the question.
	ack := make([]byte, 3)
	if _, err := io.ReadFull(conn, ack); err != nil || string(ack) != "ok\n" {
		return errors.New("agentlink: the runner did not acknowledge the message")
	}
	return nil
}

// Server is the runner's end of the channel.
type Server struct {
	path     string
	tempDir  string
	listener net.Listener
	handle   func(Message)
	wg       sync.WaitGroup
	mu       sync.Mutex
	received int
	closed   bool
}

// Listen opens the socket at path — or, when path is too long for a Unix
// socket, in a short private temporary directory — and calls handle for every
// message until Close. handle may be called from several goroutines.
func Listen(path string, handle func(Message)) (*Server, error) {
	s := &Server{path: path, handle: handle}
	if len(path) > maxSocketPath {
		dir, err := os.MkdirTemp("", "trl")
		if err != nil {
			return nil, err
		}
		s.tempDir, s.path = dir, filepath.Join(dir, "agent.sock")
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(s.path) // a socket left by a run that died
	listener, err := net.Listen("unix", s.path)
	if err != nil {
		s.cleanup()
		return nil, err
	}
	_ = os.Chmod(s.path, 0o600)
	s.listener = listener
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Path is where the socket actually is: what TRILHA_RUN_SOCKET must say.
func (s *Server) Path() string { return s.path }

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.read(conn)
		}()
	}
}

func (s *Server) read(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), MaxMessage+1)
	for scanner.Scan() {
		var message Message
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.Type == "" {
			continue
		}
		s.mu.Lock()
		s.received++
		over := s.received > MaxMessages
		s.mu.Unlock()
		if over {
			return
		}
		s.handle(message)
		if _, err := conn.Write([]byte("ok\n")); err != nil {
			return
		}
	}
}

// Close stops listening, waits for messages in flight and removes the socket.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	err := s.listener.Close()
	s.wg.Wait()
	s.cleanup()
	return err
}

func (s *Server) cleanup() {
	_ = os.Remove(s.path)
	if s.tempDir != "" {
		_ = os.RemoveAll(s.tempDir)
	}
}

// ReadHook reads a hook payload from r within ctx; a writer that never closes
// stdin costs at most the deadline.
func ReadHook(ctx context.Context, read func() ([]byte, error)) ([]byte, error) {
	type answer struct {
		data []byte
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		data, err := read()
		done <- answer{data, err}
	}()
	select {
	case a := <-done:
		return a.data, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// claudeEvents maps Claude Code hook names onto the generic vocabulary.
var claudeEvents = map[string]execution.AgentEvent{
	"SessionStart":      execution.EventSessionStart,
	"UserPromptSubmit":  execution.EventPromptSubmit,
	"PreToolUse":        execution.EventToolStart,
	"PostToolUse":       execution.EventToolEnd,
	"PermissionRequest": execution.EventPermissionRequest,
	"Stop":              execution.EventStop,
	"StopFailure":       execution.EventStopFailure,
	"SessionEnd":        execution.EventSessionEnd,
}

// ClaudeHooks are the Claude Code hook events the runner subscribes to.
var ClaudeHooks = []string{"PreToolUse", "PostToolUse", "Stop", "StopFailure"}

// FromClaudeHook translates a Claude Code hook payload (the JSON on the hook's
// stdin: hook_event_name, session_id and, for StopFailure, error) into a
// message. It answers false for anything it does not know.
func FromClaudeHook(payload []byte) (Message, bool) {
	var hook struct {
		Event     string          `json:"hook_event_name"`
		SessionID string          `json:"session_id"`
		Error     json.RawMessage `json:"error"`
	}
	if json.Unmarshal(payload, &hook) != nil {
		return Message{}, false
	}
	event, ok := claudeEvents[hook.Event]
	if !ok {
		return Message{}, false
	}
	message := Message{Type: TypeEvent, Event: event, SessionID: hook.SessionID}
	if event == execution.EventStopFailure {
		message.Text = describeError(hook.Error)
	}
	return message, true
}

// describeError turns StopFailure's error (a string, or an object with a type
// or message) into a short reason.
func describeError(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return "provider error: " + execution.Truncate(text, 200)
	}
	var object struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &object) == nil && (object.Type != "" || object.Message != "") {
		return "provider error: " + execution.Truncate(strings.TrimSpace(object.Type+" "+object.Message), 200)
	}
	return "provider error"
}
