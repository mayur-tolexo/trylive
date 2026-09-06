// Package sandbox is the client for the NeevCloud sandbox platform: the
// control plane (create, restore, snapshot, ports, lifecycle) and the
// per-sandbox data plane (exec, files, processes, PTY). Everything the
// builder and sessions need from the platform goes through the Client
// interface so tests can substitute a fake.
package sandbox

import (
	"context"
	"errors"
	"io"
	"time"
)

// Phases and snapshot statuses as reported by the platform.
const (
	PhaseReady  = "Ready"
	PhasePaused = "Paused"

	SnapshotReady  = "Ready"
	SnapshotFailed = "Failed"
)

// ErrBusy means the platform refused a create for capacity or quota reasons;
// callers should queue or retry rather than fail the user.
var ErrBusy = errors.New("sandbox: platform busy")

// ErrNotFound means the sandbox or snapshot no longer exists.
var ErrNotFound = errors.New("sandbox: not found")

// Resources is the sandbox size. Restores must match the snapshot's size, so
// every build uses one fixed value.
type Resources struct {
	CPU      float64 `json:"cpu"`
	MemoryGB int     `json:"memory_gb"`
	DiskGB   int     `json:"disk_gb"`
}

// Egress is the outbound network policy.
type Egress struct {
	Mode          string `json:"mode"` // deny_all | allow_list
	AllowInternet bool   `json:"allow_internet,omitempty"`
}

// Lifecycle controls idle and lifetime handling.
type Lifecycle struct {
	IdleTimeoutSeconds int    `json:"idle_timeout_seconds,omitempty"`
	MaxLifetimeSeconds int    `json:"max_lifetime_seconds,omitempty"`
	OnIdle             string `json:"on_idle,omitempty"` // pause | delete
}

// CreateSpec describes a new sandbox. Restore, when set, is a snapshot id
// and the sandbox comes up from that snapshot instead of the base image.
type CreateSpec struct {
	Name      string
	Restore   string
	Resources *Resources
	Egress    Egress
	Lifecycle Lifecycle
}

// Sandbox is the platform record the service cares about.
type Sandbox struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Phase      string    `json:"phase"`
	Region     string    `json:"region"`
	ConnectURL string    `json:"connect_url"`
	CreatedAt  time.Time `json:"created_at"`
}

// Snapshot is a captured sandbox state.
type Snapshot struct {
	ID        string `json:"id"`
	SandboxID string `json:"sandbox_id"`
	Status    string `json:"status"`
	Error     string `json:"error_message"`
	SizeBytes int64  `json:"size_bytes"`
}

// ExecResult is the collected output of one command.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExecSpec is one command run inside the sandbox.
type ExecSpec struct {
	Command   string
	Args      []string
	Cwd       string
	Env       []string // KEY=VALUE
	TimeoutMS int
	Stdin     string
}

// Process is a long-running program started through the processes API.
type Process struct {
	ID    string `json:"process_id"`
	State string `json:"state"`
}

// LogChunk is one piece of a process's combined output stream.
type LogChunk struct {
	Stream string // stdout | stderr
	Data   []byte
}

// Client is everything the service needs from the platform.
type Client interface {
	// Control plane.
	Create(ctx context.Context, spec CreateSpec) (Sandbox, error)
	Get(ctx context.Context, id string) (Sandbox, error)
	// WaitReady polls Get until the sandbox is Ready with a connect URL.
	WaitReady(ctx context.Context, id string, timeout time.Duration) (Sandbox, error)
	Delete(ctx context.Context, id string) error
	ExposePort(ctx context.Context, id string, port int) (previewURL string, err error)
	Keepalive(ctx context.Context, id string) error
	// Pause stops the sandbox's runtime while preserving its state and any
	// snapshots it owns.
	Pause(ctx context.Context, id string) error
	CreateSnapshot(ctx context.Context, id, name string) (Snapshot, error)
	GetSnapshot(ctx context.Context, snapshotID string) (Snapshot, error)
	// WaitSnapshot polls GetSnapshot until Ready or Failed.
	WaitSnapshot(ctx context.Context, snapshotID string, timeout time.Duration) (Snapshot, error)

	// Data plane, addressed by the sandbox's connect URL.
	Exec(ctx context.Context, sb Sandbox, spec ExecSpec, out io.Writer) (ExecResult, error)
	WriteFile(ctx context.Context, sb Sandbox, path string, data []byte) error
	StartProcess(ctx context.Context, sb Sandbox, spec ExecSpec) (Process, error)
	// StreamLogs follows a process's output, calling fn per chunk until the
	// process exits or ctx ends.
	StreamLogs(ctx context.Context, sb Sandbox, processID string, fn func(LogChunk)) error
	// DialPTY opens the interactive terminal websocket; the returned conn
	// speaks the platform's PTY protocol (binary I/O, JSON control frames).
	DialPTY(ctx context.Context, sb Sandbox, opts PTYOptions) (PTYConn, error)
}

// PTYOptions configures a terminal session.
type PTYOptions struct {
	Program string
	Args    []string
	Cols    int
	Rows    int
}

// PTYConn is a minimal websocket abstraction so the relay can be tested
// without a real socket.
type PTYConn interface {
	// Read returns the next frame; binary is true for raw terminal bytes and
	// false for a JSON control frame.
	Read(ctx context.Context) (data []byte, binary bool, err error)
	Write(ctx context.Context, data []byte, binary bool) error
	Close() error
}
