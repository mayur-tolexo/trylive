package sandbox

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory Client for tests. Exec results are scripted by
// command prefix; everything else succeeds immediately and is recorded.
type Fake struct {
	mu        sync.Mutex
	nextID    int
	Sandboxes map[string]Sandbox
	Snapshots map[string]Snapshot
	Ports     map[string][]int
	Files     map[string]map[string]string
	Calls     []string
	// ExecScript maps "program arg..." prefixes to results; the longest
	// matching prefix wins. Unmatched commands exit 0 with empty output.
	ExecScript map[string]ExecResult
	// ExecHook, when set, may override a result after script lookup.
	ExecHook func(spec ExecSpec, n int) (ExecResult, bool)
	// CreateErr makes Create fail (e.g. ErrBusy).
	CreateErr error
	// SnapshotFails makes every snapshot end Failed.
	SnapshotFails bool
	Deleted       []string
	execCount     int
}

// NewFake returns an empty fake platform.
func NewFake() *Fake {
	return &Fake{Sandboxes: map[string]Sandbox{}, Snapshots: map[string]Snapshot{}, Ports: map[string][]int{}, Files: map[string]map[string]string{}, ExecScript: map[string]ExecResult{}}
}

func (f *Fake) record(format string, args ...any) {
	f.Calls = append(f.Calls, fmt.Sprintf(format, args...))
}

func (f *Fake) Create(_ context.Context, spec CreateSpec) (Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return Sandbox{}, f.CreateErr
	}
	f.nextID++
	id := fmt.Sprintf("sb-%d", f.nextID)
	sb := Sandbox{ID: id, Name: spec.Name, Phase: PhaseReady, Region: "test", ConnectURL: "http://" + id + ".test", CreatedAt: time.Now()}
	f.Sandboxes[id] = sb
	f.Files[id] = map[string]string{}
	f.record("create %s restore=%s egress=%s/%v idle=%d max=%d on_idle=%s", spec.Name, spec.Restore, spec.Egress.Mode, spec.Egress.AllowInternet, spec.Lifecycle.IdleTimeoutSeconds, spec.Lifecycle.MaxLifetimeSeconds, spec.Lifecycle.OnIdle)
	return sb, nil
}

func (f *Fake) Get(_ context.Context, id string) (Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sb, ok := f.Sandboxes[id]
	if !ok {
		return Sandbox{}, ErrNotFound
	}
	return sb, nil
}

func (f *Fake) WaitReady(ctx context.Context, id string, _ time.Duration) (Sandbox, error) {
	return f.Get(ctx, id)
}

func (f *Fake) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.Sandboxes, id)
	f.Deleted = append(f.Deleted, id)
	f.record("delete %s", id)
	return nil
}

func (f *Fake) ExposePort(_ context.Context, id string, port int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Sandboxes[id]; !ok {
		return "", ErrNotFound
	}
	f.Ports[id] = append(f.Ports[id], port)
	f.record("expose %s %d", id, port)
	return fmt.Sprintf("https://%d-%s.test.example", port, id), nil
}

func (f *Fake) Keepalive(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("keepalive %s", id)
	return nil
}

func (f *Fake) UpdateTimeout(_ context.Context, id string, lc Lifecycle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("timeout %s idle=%d on_idle=%s", id, lc.IdleTimeoutSeconds, lc.OnIdle)
	return nil
}

func (f *Fake) CreateSnapshot(_ context.Context, id, name string) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Sandboxes[id]; !ok {
		return Snapshot{}, ErrNotFound
	}
	f.nextID++
	snap := Snapshot{ID: fmt.Sprintf("snap-%d", f.nextID), SandboxID: id, Status: SnapshotReady, SizeBytes: 15 << 20}
	if f.SnapshotFails {
		snap.Status, snap.Error = SnapshotFailed, "capture failed"
	}
	f.Snapshots[snap.ID] = snap
	f.record("snapshot %s %s", id, name)
	return snap, nil
}

func (f *Fake) GetSnapshot(_ context.Context, id string) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.Snapshots[id]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	return s, nil
}

func (f *Fake) WaitSnapshot(ctx context.Context, id string, _ time.Duration) (Snapshot, error) {
	s, err := f.GetSnapshot(ctx, id)
	if err != nil {
		return s, err
	}
	if s.Status == SnapshotFailed {
		return s, fmt.Errorf("snapshot %s failed: %s", id, s.Error)
	}
	return s, nil
}

func (f *Fake) Exec(_ context.Context, sb Sandbox, spec ExecSpec, out io.Writer) (ExecResult, error) {
	f.mu.Lock()
	f.execCount++
	n := f.execCount
	line := strings.TrimSpace(spec.Command + " " + strings.Join(spec.Args, " "))
	f.record("exec %s cwd=%s", line, spec.Cwd)
	res := ExecResult{}
	best := -1
	for prefix, r := range f.ExecScript {
		if strings.HasPrefix(line, prefix) && len(prefix) > best {
			best, res = len(prefix), r
		}
	}
	hook := f.ExecHook
	f.mu.Unlock()
	if hook != nil {
		if r, ok := hook(spec, n); ok {
			res = r
		}
	}
	if out != nil {
		io.WriteString(out, res.Stdout+res.Stderr)
	}
	return res, nil
}

func (f *Fake) WriteFile(_ context.Context, sb Sandbox, path string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Files[sb.ID] == nil {
		f.Files[sb.ID] = map[string]string{}
	}
	f.Files[sb.ID][path] = string(data)
	return nil
}

func (f *Fake) StartProcess(_ context.Context, sb Sandbox, spec ExecSpec) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("start %s %s cwd=%s", spec.Command, strings.Join(spec.Args, " "), spec.Cwd)
	return Process{ID: "proc-1", State: "running"}, nil
}

func (f *Fake) StreamLogs(ctx context.Context, _ Sandbox, _ string, fn func(LogChunk)) error {
	fn(LogChunk{Stream: "stdout", Data: []byte("server started\n")})
	<-ctx.Done()
	return nil
}

func (f *Fake) DialPTY(_ context.Context, sb Sandbox, opts PTYOptions) (PTYConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("pty %s %s", sb.ID, opts.Program)
	return NewPipePTY(), nil
}

// PipePTY is an in-memory PTYConn that echoes what it is written for relay
// tests: binary input comes back as binary output prefixed with "echo:".
type PipePTY struct {
	in     chan frame
	closed chan struct{}
	once   sync.Once
}

type frame struct {
	data   []byte
	binary bool
}

// NewPipePTY returns an echoing PTY.
func NewPipePTY() *PipePTY {
	return &PipePTY{in: make(chan frame, 64), closed: make(chan struct{})}
}

func (p *PipePTY) Read(ctx context.Context) ([]byte, bool, error) {
	select {
	case fr := <-p.in:
		return fr.data, fr.binary, nil
	case <-p.closed:
		return nil, false, io.EOF
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func (p *PipePTY) Write(_ context.Context, data []byte, binary bool) error {
	select {
	case <-p.closed:
		return io.EOF
	default:
	}
	if binary {
		p.in <- frame{append([]byte("echo:"), data...), true}
	} else {
		p.in <- frame{append([]byte("ctl:"), data...), false}
	}
	return nil
}

func (p *PipePTY) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

// CallLog returns a copy of the recorded calls.
func (f *Fake) CallLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...)
}

// DeletedIDs returns a copy of the sandbox ids deleted so far.
func (f *Fake) DeletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Deleted...)
}

// SnapshotCount returns how many snapshots were taken.
func (f *Fake) SnapshotCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Snapshots)
}
