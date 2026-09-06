// Package builder turns a repository commit into a ready-to-restore snapshot:
// it clones the repo in a build sandbox, works out a recipe, installs, starts
// the app, finds its port, snapshots the running sandbox, and leaves that
// sandbox paused as the snapshot's owner. Visitors attached to a build see
// its log as it happens.
package builder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mayur-tolexo/trylive/internal/fanout"
	"github.com/mayur-tolexo/trylive/internal/recipe"
	"github.com/mayur-tolexo/trylive/internal/repo"
	"github.com/mayur-tolexo/trylive/internal/sandbox"
	"github.com/mayur-tolexo/trylive/internal/store"
)

// Size is the one sandbox shape every build and restore uses; restores must
// match their snapshot's size, so it never varies.
var Size = sandbox.Resources{CPU: 1, MemoryGB: 2, DiskGB: 10}

// Budgets for each phase; the build fails rather than hang.
const (
	readyTimeout    = 2 * time.Minute
	cloneTimeout    = 5 * time.Minute
	installTimeout  = 10 * time.Minute
	probeTimeout    = 90 * time.Second
	snapshotTimeout = 5 * time.Minute
	buildTimeout    = 20 * time.Minute
)

// repoDir is where the clone lives inside the sandbox workspace.
const repoDir = "repo"

// Event is one item on a build's stream: a log line or a status change.
type Event struct {
	Type  string      `json:"type"` // log | status
	TS    time.Time   `json:"ts"`
	Phase string      `json:"phase,omitempty"`
	Line  string      `json:"line,omitempty"`
	Build store.Build `json:"-"`
}

// Builder runs builds and fans their events out to subscribers.
type Builder struct {
	Sandbox     sandbox.Client
	Store       store.Store
	LLM         recipe.LLM // optional; nil disables model detection and repair
	Log         *slog.Logger
	Concurrency int
	Now         func() time.Time
	// ProbeTimeout bounds how long to wait for the app to listen; defaults to
	// probeTimeout and is shortened by tests.
	ProbeTimeout time.Duration

	mu     sync.Mutex
	runs   map[string]*run
	sem    chan struct{}
	initMu sync.Once
}

// run is an in-progress build's live state.
type run struct {
	mu    sync.Mutex
	build store.Build
	log   *fanout.Log[Event]
}

// init prepares maps and the concurrency semaphore lazily.
func (b *Builder) init() {
	b.initMu.Do(func() {
		b.runs = map[string]*run{}
		n := b.Concurrency
		if n < 1 {
			n = 5
		}
		b.sem = make(chan struct{}, n)
		if b.Log == nil {
			b.Log = slog.Default()
		}
		if b.Now == nil {
			b.Now = time.Now
		}
		if b.ProbeTimeout == 0 {
			b.ProbeTimeout = probeTimeout
		}
	})
}

// Ensure returns the build for the commit, creating and starting one when
// none exists. Concurrent callers for the same commit share one build.
func (b *Builder) Ensure(ctx context.Context, info repo.Info) (store.Build, error) {
	b.init()
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing, err := b.Store.GetBuildBySHA(ctx, info.Owner, info.Name, info.SHA); err == nil {
		// A build left "building" by a crashed process is restarted rather than
		// trusted; a live one is simply returned.
		if _, live := b.runs[existing.ID]; live || existing.Status == store.BuildReady || existing.Status == store.BuildFailed || existing.Status == store.BuildUnsupported {
			return existing, nil
		}
		existing.Status = store.BuildQueued
		if err := b.Store.UpdateBuild(ctx, &existing); err != nil {
			return store.Build{}, err
		}
		b.start(existing, info)
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Build{}, err
	}
	bld := store.Build{Owner: info.Owner, Repo: info.Name, SHA: info.SHA, Status: store.BuildQueued, CreatedAt: b.Now(), LastVisitAt: b.Now()}
	if err := b.Store.CreateBuild(ctx, &bld); err != nil {
		return store.Build{}, err
	}
	b.start(bld, info)
	return bld, nil
}

// start registers the run and launches the pipeline. Caller holds b.mu.
func (b *Builder) start(bld store.Build, info repo.Info) {
	r := &run{build: bld, log: fanout.New[Event]()}
	b.runs[bld.ID] = r
	go b.execute(r, info)
}

// Subscribe streams a build's events: the history so far is replayed first,
// then live events until the build ends and the channel closes. For a
// finished build the stored log is replayed and the channel closed at once.
func (b *Builder) Subscribe(ctx context.Context, buildID string) (<-chan Event, error) {
	b.init()
	ch := make(chan Event, 256)
	b.mu.Lock()
	r, live := b.runs[buildID]
	b.mu.Unlock()
	if live {
		return r.log.Subscribe(ctx), nil
	}
	bld, err := b.Store.GetBuild(ctx, buildID)
	if err != nil {
		return nil, err
	}
	go func() {
		defer close(ch)
		for _, line := range strings.Split(strings.TrimRight(bld.Log, "\n"), "\n") {
			if line == "" {
				continue
			}
			select {
			case ch <- Event{Type: "log", TS: bld.UpdatedAt, Line: line}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case ch <- Event{Type: "status", TS: bld.UpdatedAt, Build: bld}:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// logger writes phase-tagged lines into the run's event stream.
type logger struct {
	r     *run
	phase string
	now   func() time.Time
}

func (l *logger) line(format string, args ...any) {
	l.r.log.Emit(Event{Type: "log", TS: l.now(), Phase: l.phase, Line: fmt.Sprintf(format, args...)})
}

// Write splits streamed command output into lines for the log.
func (l *logger) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			l.r.log.Emit(Event{Type: "log", TS: l.now(), Phase: l.phase, Line: line})
		}
	}
	return len(p), nil
}

// text renders the run's log lines for storage.
func (r *run) text() string {
	var sb strings.Builder
	for _, ev := range r.log.Events() {
		if ev.Type == "log" {
			if ev.Phase != "" {
				sb.WriteString("[" + ev.Phase + "] ")
			}
			sb.WriteString(ev.Line)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// execute runs the whole pipeline for one build and records the outcome.
func (b *Builder) execute(r *run, info repo.Info) {
	b.sem <- struct{}{}
	defer func() { <-b.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()

	lg := &logger{r: r, now: b.Now}
	setStatus := func(status string) {
		r.mu.Lock()
		r.build.Status = status
		bld := r.build
		r.mu.Unlock()
		if err := b.Store.UpdateBuild(ctx, &bld); err != nil {
			b.Log.Error("update build", "id", bld.ID, "err", err)
		}
		r.log.Emit(Event{Type: "status", TS: b.Now(), Build: bld})
	}
	fail := func(status, phase string, err error) {
		lg.phase = phase
		lg.line("%v", err)
		text := r.text()
		r.mu.Lock()
		r.build.Error = phase + ": " + err.Error()
		r.build.Log = text
		r.mu.Unlock()
		setStatus(status)
		r.log.Finish()
		b.mu.Lock()
		delete(b.runs, r.build.ID)
		b.mu.Unlock()
	}

	setStatus(store.BuildBuilding)

	// Build sandbox: internet for the install, paused (not deleted) when idle
	// because it must outlive the build as the snapshot's owner.
	lg.phase = "sandbox"
	lg.line("creating build sandbox")
	// The name carries the build id: a rebuild of the same commit (after a
	// store reset, or a forced rebuild) must not collide with an older golden.
	sb, err := b.Sandbox.Create(ctx, sandbox.CreateSpec{
		Name:      "tl-" + shortName(info) + "-" + info.SHA[:7] + "-" + r.build.ID[:6],
		Resources: &Size,
		Egress:    sandbox.Egress{Mode: "allow_list", AllowInternet: true},
		Lifecycle: sandbox.Lifecycle{IdleTimeoutSeconds: int(buildTimeout.Seconds()), OnIdle: "pause"},
	})
	if err != nil {
		fail(store.BuildFailed, "sandbox", err)
		return
	}
	r.mu.Lock()
	r.build.GoldenSandboxID = sb.ID
	r.mu.Unlock()
	// Any failure from here on deletes the sandbox; success keeps it as golden.
	keep := false
	defer func() {
		if !keep {
			if err := b.Sandbox.Delete(context.Background(), sb.ID); err != nil {
				b.Log.Warn("delete failed build sandbox", "id", sb.ID, "err", err)
			}
		}
	}()
	if sb, err = b.Sandbox.WaitReady(ctx, sb.ID, readyTimeout); err != nil {
		fail(store.BuildFailed, "sandbox", err)
		return
	}
	lg.line("sandbox %s ready", sb.ID)

	lg.phase = "clone"
	lg.line("git clone --depth 1 --branch %s %s", info.DefaultBranch, info.CloneURL)
	res, err := b.Sandbox.Exec(ctx, sb, sandbox.ExecSpec{Command: "git", Args: []string{"clone", "--depth", "1", "--branch", info.DefaultBranch, info.CloneURL, repoDir}, TimeoutMS: int(cloneTimeout.Milliseconds())}, lg)
	if err != nil {
		fail(store.BuildFailed, "clone", err)
		return
	}
	if res.ExitCode != 0 {
		fail(store.BuildFailed, "clone", fmt.Errorf("git clone exited %d", res.ExitCode))
		return
	}

	lg.phase = "detect"
	manifest, err := b.inspect(ctx, sb)
	if err != nil {
		fail(store.BuildFailed, "detect", err)
		return
	}
	rcp, err := recipe.Detect(manifest)
	if errors.Is(err, recipe.ErrNoMatch) {
		lg.line("no detector matched; asking the model")
		rcp, err = recipe.Infer(ctx, b.LLM, manifest)
	}
	if err != nil {
		fail(store.BuildUnsupported, "detect", fmt.Errorf("could not work out how to run this repository: %v", err))
		return
	}
	lg.line("recipe from %s: install=%q start=%q port=%d", rcp.Detector, rcp.Install, rcp.Start, rcp.Port)
	r.mu.Lock()
	rc := rcp
	r.build.Recipe = &rc
	r.mu.Unlock()

	lg.phase = "install"
	// A toolchain the image lacks is a clear "not yet", not a failed build.
	if missing := b.missingTools(ctx, sb, rcp.Install); missing != "" {
		fail(store.BuildUnsupported, "install", fmt.Errorf("%s is not available in the sandbox image yet (it has Node and Python)", missing))
		return
	}
	if err := b.install(ctx, sb, &rcp, manifest, lg); err != nil {
		fail(store.BuildFailed, "install", err)
		return
	}

	lg.phase = "start"
	if rcp.Start != "" {
		if missing := b.missingTools(ctx, sb, []string{rcp.Start}); missing != "" {
			fail(store.BuildUnsupported, "start", fmt.Errorf("%s is not available in the sandbox image yet (it has Node and Python)", missing))
			return
		}
	}
	kind, port, err := b.startAndProbe(ctx, sb, rcp, lg)
	if err != nil {
		fail(store.BuildFailed, "start", err)
		return
	}
	rcp.Kind, rcp.Port = kind, port
	if kind == recipe.KindWeb {
		lg.line("listening on port %d", port)
	} else {
		lg.line("nothing listening; visitors get a terminal in the installed repo")
	}

	lg.phase = "snapshot"
	lg.line("snapshotting the running sandbox")
	snap, err := b.Sandbox.CreateSnapshot(ctx, sb.ID, "tl-"+shortName(info)+"-"+info.SHA[:7]+"-"+r.build.ID[:6])
	if err != nil {
		fail(store.BuildFailed, "snapshot", err)
		return
	}
	if snap, err = b.Sandbox.WaitSnapshot(ctx, snap.ID, snapshotTimeout); err != nil {
		fail(store.BuildFailed, "snapshot", err)
		return
	}
	lg.line("snapshot %s ready (%d MB)", snap.ID, snap.SizeBytes>>20)

	// Pause the golden now: it costs nothing paused and is never deleted while
	// its snapshot is wanted. A failed pause is logged, not fatal.
	if err := b.Sandbox.Pause(ctx, sb.ID); err != nil {
		b.Log.Warn("pause golden", "id", sb.ID, "err", err)
	}
	keep = true

	now := b.Now()
	text := r.text()
	r.mu.Lock()
	r.build.Recipe = &rcp
	r.build.Kind, r.build.Port, r.build.SnapshotID, r.build.BuiltAt = kind, port, snap.ID, &now
	r.build.Log = text
	r.mu.Unlock()
	setStatus(store.BuildReady)
	r.log.Finish()
	b.mu.Lock()
	delete(b.runs, r.build.ID)
	b.mu.Unlock()
}

// missingTools returns the first command program that is not on the sandbox
// PATH, or "" when all are present. Programs installed by an earlier command
// (pip-provided servers) are checked only when their command is about to run.
func (b *Builder) missingTools(ctx context.Context, sb sandbox.Sandbox, cmds []string) string {
	for _, c := range cmds {
		_, prog, _ := recipe.Split(c)
		if prog == "" {
			continue
		}
		res, err := b.Sandbox.Exec(ctx, sb, sandbox.ExecSpec{Command: "sh", Args: []string{"-c", "command -v " + prog}, TimeoutMS: 5000}, nil)
		if err == nil && res.ExitCode != 0 {
			return prog
		}
	}
	return ""
}

// inspect writes the inspector into the sandbox and parses its manifest.
func (b *Builder) inspect(ctx context.Context, sb sandbox.Sandbox) (recipe.Manifest, error) {
	if err := b.Sandbox.WriteFile(ctx, sb, recipe.InspectorFile, []byte(recipe.Inspector)); err != nil {
		return recipe.Manifest{}, err
	}
	res, err := b.Sandbox.Exec(ctx, sb, sandbox.ExecSpec{Command: "python3", Args: []string{recipe.InspectorFile, repoDir}, TimeoutMS: 30000}, nil)
	if err != nil {
		return recipe.Manifest{}, err
	}
	if res.ExitCode != 0 {
		return recipe.Manifest{}, fmt.Errorf("inspector exited %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	var m recipe.Manifest
	if err := json.Unmarshal([]byte(res.Stdout), &m); err != nil {
		return recipe.Manifest{}, fmt.Errorf("inspector output: %w", err)
	}
	return m, nil
}

// install runs the recipe's commands, giving the model up to two chances to
// replace the remaining commands after a failure.
func (b *Builder) install(ctx context.Context, sb sandbox.Sandbox, rcp *recipe.Recipe, m recipe.Manifest, lg *logger) error {
	deadline := time.Now().Add(installTimeout)
	cmds := append([]string(nil), rcp.Install...)
	repairs := 0
	for i := 0; i < len(cmds); i++ {
		c := cmds[i]
		lg.line("$ %s", c)
		env, prog, args := recipe.Split(c)
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("install exceeded its time budget")
		}
		var out strings.Builder
		res, err := b.Sandbox.Exec(ctx, sb, sandbox.ExecSpec{
			Command: prog, Args: args, Cwd: cwd(rcp), Env: append(rcp.EnvList(), env...), TimeoutMS: int(remaining.Milliseconds()),
		}, teeWriter{lg, &out})
		if err != nil {
			return err
		}
		if res.ExitCode == 0 {
			continue
		}
		if b.LLM == nil || repairs >= 2 {
			return fmt.Errorf("%q exited %d", c, res.ExitCode)
		}
		repairs++
		lg.line("%q exited %d; asking the model for a fix (%d/2)", c, res.ExitCode, repairs)
		fixed, ferr := recipe.Repair(ctx, b.LLM, m, *rcp, c, out.String())
		if ferr != nil {
			return fmt.Errorf("%q exited %d and repair failed: %v", c, res.ExitCode, ferr)
		}
		// Replace this and the remaining commands with the model's list.
		cmds = append(cmds[:i], fixed...)
		rcp.Install = append([]string(nil), cmds...)
		i--
	}
	return nil
}

// startAndProbe launches the start command and watches for a listening port.
// It returns the kind (web when a port appears) and the port.
func (b *Builder) startAndProbe(ctx context.Context, sb sandbox.Sandbox, rcp recipe.Recipe, lg *logger) (string, int, error) {
	if rcp.Start == "" {
		return recipe.KindTerminal, 0, nil
	}
	lg.line("$ %s", rcp.Start)
	env, prog, args := recipe.Split(rcp.Start)
	proc, err := b.Sandbox.StartProcess(ctx, sb, sandbox.ExecSpec{Command: prog, Args: args, Cwd: cwd(&rcp), Env: append(rcp.EnvList(), env...)})
	if err != nil {
		return "", 0, err
	}
	// Stream the server's output into the log until the build ends, tagged
	// "app" on its own logger so it never races the build loop's phase.
	appLog := &logger{r: lg.r, phase: "app", now: lg.now}
	logCtx, stopLogs := context.WithCancel(ctx)
	defer stopLogs()
	go b.Sandbox.StreamLogs(logCtx, sb, proc.ID, func(c sandbox.LogChunk) { appLog.Write(c.Data) })

	deadline := time.Now().Add(b.ProbeTimeout)
	settle := false
	for {
		res, err := b.Sandbox.Exec(ctx, sb, sandbox.ExecSpec{Command: "sh", Args: []string{"-c", "ss -ltnH 2>/dev/null || cat /proc/net/tcp /proc/net/tcp6 2>/dev/null"}, TimeoutMS: 5000}, nil)
		if err != nil {
			return "", 0, err
		}
		listening := ParseListening(res.Stdout)
		if port := ChoosePort(rcp.Port, listening); port > 0 {
			// Dev servers often open helper ports before the page server; unless the
			// hinted port is already up, look once more before committing.
			if port == rcp.Port || settle {
				return recipe.KindWeb, port, nil
			}
			settle = true
		}
		if time.Now().After(deadline) {
			// The recipe promised a server that never listened; treat the repo as
			// installed-but-not-serving rather than failing the whole build.
			return recipe.KindTerminal, 0, nil
		}
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(min(2*time.Second, b.ProbeTimeout/4+time.Millisecond)):
		}
	}
}

// cwd is the working directory for recipe commands inside the sandbox.
func cwd(r *recipe.Recipe) string {
	if r.Cwd != "" {
		return repoDir + "/" + strings.Trim(r.Cwd, "/")
	}
	return repoDir
}

// shortName makes a DNS-safe fragment from the repo for sandbox names.
func shortName(info repo.Info) string {
	s := strings.ToLower(info.Owner + "-" + info.Name)
	var out []rune
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			out = append(out, c)
		}
	}
	name := strings.Trim(string(out), "-")
	// Leave room for "tl-", the sha, and the build id within the 63-char limit.
	if len(name) > 36 {
		name = name[:36]
	}
	return strings.Trim(name, "-")
}

// teeWriter sends command output to the log and a capture buffer.
type teeWriter struct {
	lg  *logger
	buf *strings.Builder
}

func (t teeWriter) Write(p []byte) (int, error) {
	t.buf.Write(p)
	return t.lg.Write(p)
}
