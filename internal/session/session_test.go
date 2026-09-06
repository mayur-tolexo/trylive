package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mayur-tolexo/trylive/internal/builder"
	"github.com/mayur-tolexo/trylive/internal/recipe"
	"github.com/mayur-tolexo/trylive/internal/repo"
	"github.com/mayur-tolexo/trylive/internal/sandbox"
	"github.com/mayur-tolexo/trylive/internal/store"
)

// fakeResolver returns fixed metadata for any ref.
type fakeResolver struct{ err error }

func (f fakeResolver) Resolve(_ context.Context, ref repo.Ref) (repo.Info, error) {
	if f.err != nil {
		return repo.Info{}, f.err
	}
	return repo.Info{Ref: ref, DefaultBranch: "main", SHA: "abcdef1234567", CloneURL: "https://github.com/" + ref.Slug() + ".git"}, nil
}

type env struct {
	m  *Manager
	f  *sandbox.Fake
	st *store.Memory
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f := sandbox.NewFake()
	manifest, _ := json.Marshal(recipe.Manifest{PackageJSON: &recipe.PackageJSON{Scripts: map[string]string{"dev": "vite"}, DevDependencies: map[string]string{"vite": "5"}}})
	f.ExecScript["python3 inspect.py"] = sandbox.ExecResult{Stdout: string(manifest)}
	f.ExecScript["sh -c ss"] = sandbox.ExecResult{Stdout: "LISTEN 0 511 0.0.0.0:5173 0.0.0.0:*\n"}
	st := store.NewMemory()
	b := &builder.Builder{Sandbox: f, Store: st}
	m := &Manager{Store: st, Sandbox: f, Builder: b, Resolver: fakeResolver{}, Poll: 20 * time.Millisecond,
		Limits: Limits{PerDevice: 1, PerIP: 2, Global: 3, TTL: 15 * time.Minute, MaxTTL: 30 * time.Minute}}
	return &env{m: m, f: f, st: st}
}

// collect drains events until the channel closes or the timeout passes.
func collect(t *testing.T, ch <-chan Event, d time.Duration) []Event {
	t.Helper()
	var out []Event
	timer := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timer:
			return out
		}
	}
}

func names(evs []Event) string {
	var n []string
	for _, e := range evs {
		n = append(n, e.Name)
	}
	return strings.Join(n, " ")
}

func TestSessionBuildsThenGoesLive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, bld, err := e.m.Create(ctx, "dev1", "1.1.1.1", "https://github.com/octo/app")
	if err != nil {
		t.Fatal(err)
	}
	if bld.Status != store.BuildQueued && bld.Status != store.BuildBuilding {
		t.Fatalf("build status %s", bld.Status)
	}
	ch, _ := e.m.Subscribe(ctx, s.ID)
	var evs []Event
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs = append(evs, collect(t, ch, 100*time.Millisecond)...)
		if strings.Contains(names(evs), "terminal") {
			break
		}
	}
	got := names(evs)
	for _, want := range []string{"phase", "log", "preview", "terminal", "ttl"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s event in: %s", want, got)
		}
	}
	live, _ := e.st.GetSession(ctx, s.ID)
	if live.Status != store.SessionLive || live.PreviewURL == "" || live.SandboxID == "" || live.ExpiresAt == nil {
		t.Fatalf("session = %+v", live)
	}
	calls := strings.Join(e.f.CallLog(), "\n")
	if !strings.Contains(calls, "restore=snap-") || !strings.Contains(calls, "egress=deny_all") || !strings.Contains(calls, "idle=900 max=1800 on_idle=delete") {
		t.Errorf("visitor sandbox spec wrong:\n%s", calls)
	}
	if !strings.Contains(calls, "expose "+live.SandboxID+" 5173") {
		t.Errorf("port not exposed:\n%s", calls)
	}
	b, _ := e.st.GetBuild(ctx, bld.ID)
	if b.Visits != 1 {
		t.Errorf("visits = %d", b.Visits)
	}

	// Extend once, then refuse.
	ext, err := e.m.Extend(ctx, s.ID)
	if err != nil || !ext.Extended || !strings.Contains(strings.Join(e.f.CallLog(), "\n"), "keepalive "+live.SandboxID) {
		t.Fatalf("Extend = %+v, %v", ext, err)
	}
	if _, err := e.m.Extend(ctx, s.ID); !errors.Is(err, ErrAlreadyExtnd) {
		t.Errorf("second extend err = %v", err)
	}

	// PTY opens against the visitor sandbox.
	conn, err := e.m.PTY(ctx, s.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if !strings.Contains(strings.Join(e.f.CallLog(), "\n"), "pty "+live.SandboxID+" bash") {
		t.Error("pty not dialled on the visitor sandbox")
	}

	// Platform reclaims the sandbox → session ends.
	e.f.Delete(ctx, live.SandboxID)
	ended := collect(t, ch, 2*time.Second)
	if !strings.Contains(names(ended), "ended") {
		t.Errorf("no ended event: %s", names(ended))
	}
	final, _ := e.st.GetSession(ctx, s.ID)
	if final.Status != store.SessionEnded || final.EndedReason != "session expired" {
		t.Errorf("final = %+v", final)
	}
	// A late subscriber to an ended session gets one ended event.
	late, _ := e.m.Subscribe(ctx, s.ID)
	if evs := collect(t, late, time.Second); len(evs) != 1 || evs[0].Name != "ended" {
		t.Errorf("late subscribe = %s", names(evs))
	}
}

func TestSecondVisitorReusesReadyBuild(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s1, _, _ := e.m.Create(ctx, "dev1", "1.1.1.1", "octo/app")
	waitLive(t, e, s1.ID)
	before := e.f.SnapshotCount()
	s2, bld, err := e.m.Create(ctx, "dev2", "2.2.2.2", "octo/app")
	if err != nil || bld.Status != store.BuildReady || s2.Status != store.SessionStarting {
		t.Fatalf("second create = %+v %+v %v", s2, bld, err)
	}
	waitLive(t, e, s2.ID)
	if e.f.SnapshotCount() != before {
		t.Error("a second snapshot was taken for the same commit")
	}
}

func waitLive(t *testing.T, e *env, id string) store.Session {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := e.st.GetSession(context.Background(), id)
		if s.Status == store.SessionLive || s.Status == store.SessionEnded {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session never went live")
	return store.Session{}
}

func TestLimitsAndErrors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, _, err := e.m.Create(ctx, "d", "ip", "not a repo"); !errors.Is(err, repo.ErrInvalidRef) {
		t.Errorf("invalid ref err = %v", err)
	}
	e.m.Create(ctx, "d1", "ip1", "octo/app")
	if _, _, err := e.m.Create(ctx, "d1", "ip9", "octo/app"); !errors.Is(err, ErrLimited) {
		t.Errorf("per-device limit err = %v", err)
	}
	e.m.Create(ctx, "d2", "ip1", "octo/app")
	if _, _, err := e.m.Create(ctx, "d3", "ip1", "octo/app"); !errors.Is(err, ErrLimited) {
		t.Errorf("per-ip limit err = %v", err)
	}
	e.m.Create(ctx, "d4", "ip4", "octo/app")
	if _, _, err := e.m.Create(ctx, "d5", "ip5", "octo/app"); !errors.Is(err, ErrBusy) {
		t.Errorf("global limit err = %v", err)
	}
	e2 := newEnv(t)
	e2.m.Resolver = fakeResolver{err: repo.ErrPrivate}
	if _, _, err := e2.m.Create(ctx, "d", "ip", "octo/secret"); !errors.Is(err, repo.ErrPrivate) {
		t.Errorf("private err = %v", err)
	}
	if _, err := e.m.Extend(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("extend missing err = %v", err)
	}
}

func TestUnsupportedBuildEndsSession(t *testing.T) {
	e := newEnv(t)
	e.f.ExecScript["python3 inspect.py"] = sandbox.ExecResult{Stdout: `{"files":["LICENSE"]}`}
	ctx := context.Background()
	s, _, _ := e.m.Create(ctx, "d", "ip", "octo/empty")
	ch, _ := e.m.Subscribe(ctx, s.ID)
	evs := collect(t, ch, 3*time.Second)
	got := names(evs)
	if !strings.Contains(got, "error") || !strings.HasSuffix(got, "ended") {
		t.Fatalf("events = %s", got)
	}
	final, _ := e.st.GetSession(ctx, s.ID)
	if final.Status != store.SessionEnded || final.EndedReason != "unsupported" {
		t.Errorf("final = %+v", final)
	}
	if _, err := e.m.PTY(ctx, s.ID, 80, 24); !errors.Is(err, ErrNotLive) {
		t.Errorf("pty on ended session err = %v", err)
	}
}

func TestRestoreBusyEndsSession(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s1, _, _ := e.m.Create(ctx, "d1", "ip1", "octo/app")
	waitLive(t, e, s1.ID)
	e.f.CreateErr = sandbox.ErrBusy
	s2, _, _ := e.m.Create(ctx, "d2", "ip2", "octo/app")
	ch, _ := e.m.Subscribe(ctx, s2.ID)
	evs := collect(t, ch, 3*time.Second)
	var code string
	for _, ev := range evs {
		if ev.Name == "error" {
			code = ev.Data.(ErrorData).Code
		}
	}
	if code != "busy" || !strings.HasSuffix(names(evs), "ended") {
		t.Errorf("events = %s code=%s", names(evs), code)
	}
}
