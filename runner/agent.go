package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/internal/agentlink"
	"github.com/emersonjoe/trilha-runner/internal/redact"
	"github.com/emersonjoe/trilha-runner/worktree"
	"github.com/emersonjoe/trilha-spec/execution"
)

// agentSession gathers what the agent tells the runner during a run — its
// state, a question, notes, an overview, an attention badge, its session id —
// from the socket (command-line agents and their hooks) and from in-process
// drivers alike. Everything that will leave the machine is size-limited and
// secret-scanned here, once.
type agentSession struct {
	mu        sync.Mutex
	secrets   []string
	runDir    string
	now       func() time.Time
	question  *execution.Question
	notes     []execution.Note
	overview  string
	sessionID string
	state     *stateReporter
	attention func(execution.Attention)
	logf      func(string, ...any)
	// report gathers findings, a verdict, replies and artifacts.
	report agentReport
}

func (a *agentSession) handle(message agentlink.Message) {
	a.mu.Lock()
	if message.SessionID != "" && len(message.SessionID) <= 128 {
		a.sessionID = message.SessionID
	}
	a.mu.Unlock()
	switch message.Type {
	case agentlink.TypeEvent:
		a.state.apply(message.Event)
		if message.Event == execution.EventStopFailure {
			reason := firstNonEmpty(message.Text, "provider error")
			a.raise(execution.Attention{Reason: reason})
		}
	case agentlink.TypeAsk:
		text := strings.TrimSpace(message.Text)
		if text == "" {
			return
		}
		a.recordQuestion(text)
		a.state.apply(execution.EventAsked)
	case agentlink.TypeAttention:
		a.raise(execution.Attention{Reason: strings.TrimSpace(message.Text), Clear: message.Clear})
	case agentlink.TypeNote:
		content, _ := redact.Scan(strings.TrimSpace(message.Text), a.secrets...)
		if content == "" {
			return
		}
		a.mu.Lock()
		a.notes = append(a.notes, execution.Note{Content: execution.Truncate(content, execution.MaxNote), Source: "ai", At: a.now().UTC()})
		if len(a.notes) > execution.MaxNotes {
			a.notes = a.notes[len(a.notes)-execution.MaxNotes:]
		}
		a.mu.Unlock()
	case agentlink.TypeOverview:
		overview, _ := redact.Scan(strings.TrimSpace(message.Text), a.secrets...)
		a.mu.Lock()
		a.overview = execution.Truncate(overview, execution.MaxOverview)
		a.mu.Unlock()
	case agentlink.TypeFinding, agentlink.TypeVerdict, agentlink.TypeReply, agentlink.TypeArtifact:
		a.handleReport(message)
	}
}

// recordQuestion keeps the question for the result. One that carries a
// secret reaches the cloud as a fixed notice; the words stay on the runner,
// in question.txt, readable by the worker's user only.
func (a *agentSession) recordQuestion(text string) {
	clean, found := redact.Scan(text, a.secrets...)
	if found {
		clean = redact.Notice
		if a.logf != nil {
			a.logf("the agent's question contained a secret; it stays in %s", filepath.Join(a.runDir, "question.txt"))
		}
	}
	if a.runDir != "" {
		_ = os.MkdirAll(a.runDir, 0o755)
		_ = os.WriteFile(filepath.Join(a.runDir, "question.txt"), []byte(text+"\n"), 0o600)
	}
	a.mu.Lock()
	a.question = &execution.Question{Text: execution.Truncate(clean, execution.MaxQuestion), AskedAt: a.now().UTC()}
	a.mu.Unlock()
}

func (a *agentSession) raise(attention execution.Attention) {
	if attention.Reason != "" {
		attention.Reason, _ = redact.Scan(attention.Reason, a.secrets...)
		attention.Reason = execution.Truncate(attention.Reason, execution.MaxAttentionReason)
	}
	if (attention.Reason == "" && !attention.Clear) || a.attention == nil {
		return
	}
	a.attention(attention)
}

// takeQuestion answers the question asked since the last call, if any.
func (a *agentSession) takeQuestion() *execution.Question {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	question := a.question
	a.question = nil
	return question
}

// hasQuestion answers whether a question is waiting to be taken.
func (a *agentSession) hasQuestion() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.question != nil
}

// memory answers the notes, the overview and the session id gathered so far.
func (a *agentSession) memory() ([]execution.Note, string, string) {
	if a == nil {
		return nil, "", ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]execution.Note(nil), a.notes...), a.overview, a.sessionID
}

// stateReporter applies the protocol's state machine to hook events and
// reports the state only when it changes, at most once per interval; a
// change inside the interval is held and sent when it ends, unless the state
// went back to what was last sent.
type stateReporter struct {
	mu        sync.Mutex
	interval  time.Duration
	now       func() time.Time
	afterFunc func(time.Duration, func()) func() bool
	send      func(execution.AgentState)
	current   execution.AgentState
	sent      execution.AgentState
	lastSent  time.Time
	pending   func() bool
}

func newStateReporter(interval time.Duration, send func(execution.AgentState)) *stateReporter {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &stateReporter{interval: interval, now: time.Now, send: send, afterFunc: func(d time.Duration, f func()) func() bool {
		return time.AfterFunc(d, f).Stop
	}}
}

func (s *stateReporter) apply(event execution.AgentEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = execution.NextAgentState(event, s.current)
	if s.current == s.sent || s.current == "" {
		return
	}
	if wait := s.interval - s.now().Sub(s.lastSent); s.lastSent.IsZero() || wait <= 0 {
		s.sendLocked()
		return
	} else if s.pending == nil {
		s.pending = s.afterFunc(wait, s.flush)
	}
}

func (s *stateReporter) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = nil
	if s.current != s.sent && s.current != "" {
		s.sendLocked()
	}
}

func (s *stateReporter) sendLocked() {
	s.sent, s.lastSent = s.current, s.now()
	if s.send != nil {
		s.send(s.current)
	}
}

// close sends a held change now: the run is ending.
func (s *stateReporter) close() {
	s.mu.Lock()
	if s.pending != nil {
		s.pending()
		s.pending = nil
	}
	s.mu.Unlock()
	s.flush()
}

// stateMessage is the human line that travels with an agent state, so a
// control plane that requires a message still takes the report.
func stateMessage(state execution.AgentState) string {
	switch state {
	case execution.AgentWorking:
		return "agent is working"
	case execution.AgentIdle:
		return "agent stopped and is idle"
	case execution.AgentWaitingPermission:
		return "agent is waiting for permission"
	case execution.AgentAsked:
		return "agent asked a question"
	case execution.AgentStoppedError:
		return "agent stopped on a provider error"
	}
	return "agent state: " + string(state)
}

// openAgentLink starts listening for the agent and tells it where (when the
// agent runs on this host: a sandboxed agent cannot reach the socket), puts
// the trilha-runner binary on its PATH, and answers the session plus the
// function that ends it — which sends a held agent state before the result.
func (r *Runner) openAgentLink(id, runDir string, env *[]string, onHost bool) (*agentSession, func()) {
	session := &agentSession{runDir: runDir, now: time.Now, logf: r.logf}
	if r.AI != nil && r.AI.Credential != "" {
		session.secrets = []string{r.AI.Credential}
	}
	session.state = newStateReporter(r.AgentStateInterval, func(state execution.AgentState) {
		r.progress(Event{AgentState: state, Message: stateMessage(state)})
	})
	session.attention = func(attention execution.Attention) {
		message := "attention: " + attention.Reason
		if attention.Clear {
			message = "attention cleared"
		}
		r.progress(Event{Attention: &attention, Message: message})
	}
	if !onHost {
		return session, session.state.close
	}
	server, err := agentlink.Listen(filepath.Join(runDir, "agent.sock"), session.handle)
	if err != nil {
		r.logf("agent link: %v (the agent cannot ask or report its state)", err)
		return session, session.state.close
	}
	*env = append(*env, agentlink.EnvSocket+"="+server.Path(), agentlink.EnvRunID+"="+firstNonEmpty(r.RunID, id))
	// Where a read-only run writes the reports it publishes: outside the
	// worktree, which must stay clean.
	scratch := filepath.Join(runDir, "scratch")
	if err := os.MkdirAll(scratch, 0o700); err == nil {
		*env = append(*env, agentlink.EnvArtifactDir+"="+scratch)
	}
	if r.Self != "" {
		bin := filepath.Join(runDir, "bin")
		link := filepath.Join(bin, "trilha-runner")
		_ = os.MkdirAll(bin, 0o755)
		_ = os.Remove(link)
		if err := os.Symlink(r.Self, link); err == nil {
			*env = append(*env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	return session, func() {
		_ = server.Close()
		session.state.close()
	}
}

// finishAgent puts what the agent left — notes, overview, session — and the
// branch's diff counts into the result.
func (r *Runner) finishAgent(ctx context.Context, res *Result, session *agentSession, manager worktree.Manager, tree worktree.Worktree, driverName string) {
	if res == nil {
		return
	}
	notes, overview, sessionID := session.memory()
	res.Notes, res.Overview = notes, overview
	if sessionID != "" && driverName == "claude-code" && driver.ValidSessionID(sessionID) {
		res.Session = &execution.Session{Driver: driverName, SessionID: sessionID}
	}
	if res.Worktree != "" {
		if stats, err := manager.NumStat(ctx, tree); err == nil {
			res.DiffStats = &stats
		}
	}
}
