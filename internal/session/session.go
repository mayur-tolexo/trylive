// Package session owns a visitor's lifecycle: resolve the repo, join or
// start its build, restore a sandbox from the build's snapshot, expose the
// app, and stream every step to the browser until the sandbox expires.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mayur-tolexo/trylive/internal/builder"
	"github.com/mayur-tolexo/trylive/internal/fanout"
	"github.com/mayur-tolexo/trylive/internal/recipe"
	"github.com/mayur-tolexo/trylive/internal/repo"
	"github.com/mayur-tolexo/trylive/internal/sandbox"
	"github.com/mayur-tolexo/trylive/internal/store"
)

// Limits bound anonymous usage.
type Limits struct {
	PerDevice int
	PerIP     int
	Global    int
	TTL       time.Duration // first window
	MaxTTL    time.Duration // after one extension
}

// DefaultLimits are the launch values.
var DefaultLimits = Limits{PerDevice: 1, PerIP: 2, Global: 200, TTL: 15 * time.Minute, MaxTTL: 30 * time.Minute}

// Typed errors the API maps to status codes.
var (
	ErrLimited      = errors.New("session: too many live sessions for this device")
	ErrBusy         = errors.New("session: capacity is full")
	ErrNotLive      = errors.New("session: sandbox is not live")
	ErrAlreadyExtnd = errors.New("session: already extended")
)

// Event is one server-sent event for the browser.
type Event struct {
	Name string `json:"-"`
	Data any    `json:"data"`
}

// Payloads for Event.Data.
type (
	LogData     struct{ TS, Phase, Line string }
	PhaseData   struct{ BuildStatus, SessionStatus, Message string }
	PreviewData struct {
		URL  string
		Port int
	}
	TerminalData struct{ Ready bool }
	TTLData      struct {
		ExpiresAt time.Time
		Extended  bool
	}
	EndedData struct{ Reason string }
	ErrorData struct{ Code, Message string }
)

// Manager creates and runs sessions.
type Manager struct {
	Store    store.Store
	Sandbox  sandbox.Client
	Builder  *builder.Builder
	Resolver repo.Resolver
	Limits   Limits
	Log      *slog.Logger
	Now      func() time.Time
	// Poll is how often a live sandbox is checked for having been reclaimed.
	Poll time.Duration

	mu     sync.Mutex
	live   map[string]*fanout.Log[Event]
	initMu sync.Once
}

func (m *Manager) init() {
	m.initMu.Do(func() {
		m.live = map[string]*fanout.Log[Event]{}
		if m.Log == nil {
			m.Log = slog.Default()
		}
		if m.Now == nil {
			m.Now = time.Now
		}
		if m.Limits == (Limits{}) {
			m.Limits = DefaultLimits
		}
		if m.Poll == 0 {
			m.Poll = 30 * time.Second
		}
	})
}

// Create resolves the repo, enforces limits, joins the build for its head
// commit, and starts the session's background flow.
func (m *Manager) Create(ctx context.Context, device, ip, repoRef string) (store.Session, store.Build, error) {
	m.init()
	ref, err := repo.Parse(repoRef)
	if err != nil {
		return store.Session{}, store.Build{}, err
	}
	byDev, byIP, total, err := m.Store.LiveSessions(ctx, device, ip)
	if err != nil {
		return store.Session{}, store.Build{}, err
	}
	if total >= m.Limits.Global {
		return store.Session{}, store.Build{}, ErrBusy
	}
	if byDev >= m.Limits.PerDevice || byIP >= m.Limits.PerIP {
		return store.Session{}, store.Build{}, ErrLimited
	}
	info, err := m.Resolver.Resolve(ctx, ref)
	if err != nil {
		return store.Session{}, store.Build{}, err
	}
	bld, err := m.Builder.Ensure(ctx, info)
	if err != nil {
		return store.Session{}, store.Build{}, err
	}
	s := store.Session{BuildID: bld.ID, Device: device, IP: ip, Status: store.SessionBuilding, CreatedAt: m.Now()}
	if bld.Status == store.BuildReady {
		s.Status = store.SessionStarting
	}
	if err := m.Store.CreateSession(ctx, &s); err != nil {
		return store.Session{}, store.Build{}, err
	}
	m.Store.RecordVisit(ctx, bld.ID, m.Now())
	l := fanout.New[Event]()
	m.mu.Lock()
	m.live[s.ID] = l
	m.mu.Unlock()
	go m.run(s, bld, l)
	return s, bld, nil
}

// Subscribe replays a session's events so far and then streams new ones
// until the session ends. Finished sessions get a single ended event.
func (m *Manager) Subscribe(ctx context.Context, id string) (<-chan Event, error) {
	m.init()
	ch := make(chan Event, 256)
	m.mu.Lock()
	l, ok := m.live[id]
	m.mu.Unlock()
	if !ok {
		s, err := m.Store.GetSession(ctx, id)
		if err != nil {
			return nil, err
		}
		go func() {
			defer close(ch)
			reason := s.EndedReason
			if reason == "" {
				reason = "server restarted"
			}
			ch <- Event{Name: "ended", Data: EndedData{Reason: reason}}
		}()
		return ch, nil
	}
	return l.Subscribe(ctx), nil
}

// run drives one session from build to expiry.
func (m *Manager) run(s store.Session, bld store.Build, l *fanout.Log[Event]) {
	ctx := context.Background()
	end := func(reason string) {
		s.Status, s.EndedReason = store.SessionEnded, reason
		if err := m.Store.UpdateSession(ctx, &s); err != nil {
			m.Log.Error("end session", "id", s.ID, "err", err)
		}
		l.Emit(Event{Name: "ended", Data: EndedData{Reason: reason}})
		l.Finish()
		m.mu.Lock()
		delete(m.live, s.ID)
		m.mu.Unlock()
	}

	// Follow the build until it settles, forwarding its log.
	if bld.Status != store.BuildReady {
		l.Emit(Event{Name: "phase", Data: PhaseData{BuildStatus: bld.Status, SessionStatus: s.Status, Message: "working out how to run this repo"}})
		events, err := m.Builder.Subscribe(ctx, bld.ID)
		if err != nil {
			l.Emit(Event{Name: "error", Data: ErrorData{Code: "infra_error", Message: err.Error()}})
			end("build unavailable")
			return
		}
		for ev := range events {
			switch ev.Type {
			case "log":
				l.Emit(Event{Name: "log", Data: LogData{TS: ev.TS.Format(time.RFC3339Nano), Phase: ev.Phase, Line: ev.Line}})
			case "status":
				bld = ev.Build
				l.Emit(Event{Name: "phase", Data: PhaseData{BuildStatus: bld.Status, SessionStatus: s.Status, Message: phaseMessage(bld.Status)}})
			}
		}
		// The stream closes when the build ends; read the final record.
		if fresh, err := m.Store.GetBuild(ctx, bld.ID); err == nil {
			bld = fresh
		}
		switch bld.Status {
		case store.BuildReady:
		case store.BuildUnsupported:
			l.Emit(Event{Name: "error", Data: ErrorData{Code: "unsupported", Message: bld.Error}})
			end("unsupported")
			return
		default:
			l.Emit(Event{Name: "error", Data: ErrorData{Code: "infra_error", Message: bld.Error}})
			end("build failed")
			return
		}
	}

	s.Status = store.SessionStarting
	m.Store.UpdateSession(ctx, &s)
	l.Emit(Event{Name: "phase", Data: PhaseData{BuildStatus: bld.Status, SessionStatus: s.Status, Message: "restoring your sandbox"}})

	// Visitor sandbox: restored from the snapshot, no egress, reclaimed by the
	// platform on idle or lifetime so a forgotten tab costs nothing for long.
	sb, err := m.Sandbox.Create(ctx, sandbox.CreateSpec{
		Name:      "tlv-" + s.ID[:8],
		Restore:   bld.SnapshotID,
		Resources: &builder.Size,
		Egress:    sandbox.Egress{Mode: "deny_all"},
		Lifecycle: sandbox.Lifecycle{IdleTimeoutSeconds: int(m.Limits.TTL.Seconds()), MaxLifetimeSeconds: int(m.Limits.MaxTTL.Seconds()), OnIdle: "delete"},
	})
	if err != nil {
		code := "infra_error"
		if errors.Is(err, sandbox.ErrBusy) {
			code = "busy"
		}
		l.Emit(Event{Name: "error", Data: ErrorData{Code: code, Message: "could not start a sandbox: " + err.Error()}})
		end("no capacity")
		return
	}
	s.SandboxID = sb.ID
	m.Store.UpdateSession(ctx, &s)
	if sb, err = m.Sandbox.WaitReady(ctx, sb.ID, 2*time.Minute); err != nil {
		m.Sandbox.Delete(ctx, sb.ID)
		l.Emit(Event{Name: "error", Data: ErrorData{Code: "infra_error", Message: err.Error()}})
		end("sandbox failed to start")
		return
	}

	if bld.Kind == recipe.KindWeb && bld.Port > 0 {
		url, err := m.Sandbox.ExposePort(ctx, sb.ID, bld.Port)
		if err != nil {
			m.Log.Warn("expose port", "session", s.ID, "err", err)
		} else {
			s.PreviewURL = url
			l.Emit(Event{Name: "preview", Data: PreviewData{URL: url, Port: bld.Port}})
		}
	}
	exp := m.Now().Add(m.Limits.TTL)
	s.Status, s.TerminalReady, s.ExpiresAt = store.SessionLive, true, &exp
	m.Store.UpdateSession(ctx, &s)
	l.Emit(Event{Name: "terminal", Data: TerminalData{Ready: true}})
	l.Emit(Event{Name: "ttl", Data: TTLData{ExpiresAt: exp, Extended: false}})
	l.Emit(Event{Name: "phase", Data: PhaseData{BuildStatus: bld.Status, SessionStatus: s.Status, Message: "live"}})

	// Watch until the platform reclaims the sandbox or the hard cap passes.
	hard := s.CreatedAt.Add(m.Limits.MaxTTL + time.Minute)
	for {
		select {
		case <-time.After(m.Poll):
		}
		cur, err := m.Sandbox.Get(ctx, sb.ID)
		switch {
		case errors.Is(err, sandbox.ErrNotFound):
			end("session expired")
			return
		case err == nil && cur.Phase != sandbox.PhaseReady:
			end("sandbox stopped")
			return
		}
		if m.Now().After(hard) {
			m.Sandbox.Delete(ctx, sb.ID)
			end("session expired")
			return
		}
	}
}

// phaseMessage is the human line for a build status.
func phaseMessage(status string) string {
	switch status {
	case store.BuildQueued:
		return "queued"
	case store.BuildBuilding:
		return "building"
	case store.BuildReady:
		return "build ready"
	case store.BuildUnsupported:
		return "we couldn't work out how to run this repo"
	default:
		return "build failed"
	}
}

// Extend grants the single allowed extension: the idle window is reset and
// the expiry moves to the hard cap.
func (m *Manager) Extend(ctx context.Context, id string) (store.Session, error) {
	m.init()
	s, err := m.Store.GetSession(ctx, id)
	if err != nil {
		return store.Session{}, err
	}
	if s.Status != store.SessionLive || s.SandboxID == "" {
		return s, ErrNotLive
	}
	if s.Extended {
		return s, ErrAlreadyExtnd
	}
	if err := m.Sandbox.Keepalive(ctx, s.SandboxID); err != nil {
		return s, err
	}
	exp := s.CreatedAt.Add(m.Limits.MaxTTL)
	if sooner := m.Now().Add(m.Limits.TTL); sooner.Before(exp) {
		exp = sooner
	}
	s.Extended, s.ExpiresAt = true, &exp
	if err := m.Store.UpdateSession(ctx, &s); err != nil {
		return s, err
	}
	m.mu.Lock()
	l := m.live[id]
	m.mu.Unlock()
	if l != nil {
		l.Emit(Event{Name: "ttl", Data: TTLData{ExpiresAt: exp, Extended: true}})
	}
	return s, nil
}

// PTY opens a terminal in the visitor's sandbox rooted in the repo.
func (m *Manager) PTY(ctx context.Context, id string, cols, rows int) (sandbox.PTYConn, error) {
	m.init()
	s, err := m.Store.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Status != store.SessionLive || s.SandboxID == "" {
		return nil, ErrNotLive
	}
	sb, err := m.Sandbox.Get(ctx, s.SandboxID)
	if err != nil {
		return nil, err
	}
	// The PTY starts in the workspace root; hop into the clone first.
	return m.Sandbox.DialPTY(ctx, sb, sandbox.PTYOptions{Program: "bash", Args: []string{"-c", "cd repo 2>/dev/null; exec bash -i"}, Cols: cols, Rows: rows})
}

// Get returns the session and its build for the API.
func (m *Manager) Get(ctx context.Context, id string) (store.Session, store.Build, error) {
	s, err := m.Store.GetSession(ctx, id)
	if err != nil {
		return store.Session{}, store.Build{}, err
	}
	b, err := m.Store.GetBuild(ctx, s.BuildID)
	if err != nil {
		return s, store.Build{}, fmt.Errorf("session %s build: %w", id, err)
	}
	return s, b, nil
}
