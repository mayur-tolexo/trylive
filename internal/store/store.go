// Package store persists builds (one per repo commit) and visitor sessions.
// Postgres in production, an in-memory implementation for development and
// tests.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/mayur-tolexo/trylive/internal/recipe"
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("store: not found")

// Build statuses.
const (
	BuildQueued      = "queued"
	BuildBuilding    = "building"
	BuildReady       = "ready"
	BuildFailed      = "failed"
	BuildUnsupported = "unsupported"
)

// Session statuses.
const (
	SessionPending  = "pending"
	SessionBuilding = "building"
	SessionStarting = "starting"
	SessionLive     = "live"
	SessionEnded    = "ended"
)

// Build is the outcome of working out how to run one commit of a repo. The
// golden sandbox stays paused because deleting it would delete the snapshot.
type Build struct {
	ID              string
	Owner           string
	Repo            string
	SHA             string
	Status          string
	Kind            string
	Recipe          *recipe.Recipe
	Port            int
	GoldenSandboxID string
	SnapshotID      string
	Log             string
	Error           string
	BuiltAt         *time.Time
	LastVisitAt     time.Time
	Visits          int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Session is one visitor's sandbox.
type Session struct {
	ID            string
	BuildID       string
	Device        string
	IP            string
	SandboxID     string
	PreviewURL    string
	Status        string
	TerminalReady bool
	Extended      bool
	CreatedAt     time.Time
	ExpiresAt     *time.Time
	EndedReason   string
}

// Live reports whether the session still holds (or is about to hold) a sandbox.
func (s Session) Live() bool { return s.Status != SessionEnded }

// Store is the persistence contract. Implementations are safe for concurrent use.
type Store interface {
	CreateBuild(ctx context.Context, b *Build) error
	// UpdateBuild overwrites every mutable field of an existing build.
	UpdateBuild(ctx context.Context, b *Build) error
	GetBuild(ctx context.Context, id string) (Build, error)
	GetBuildBySHA(ctx context.Context, owner, repo, sha string) (Build, error)
	// LatestBuild returns the newest build for the repo regardless of status.
	LatestBuild(ctx context.Context, owner, repo string) (Build, error)
	// RecordVisit bumps the visit counter and last-visit time.
	RecordVisit(ctx context.Context, buildID string, at time.Time) error

	CreateSession(ctx context.Context, s *Session) error
	UpdateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	// LiveSessions counts sessions not yet ended for a device, for an IP, and overall.
	LiveSessions(ctx context.Context, device, ip string) (byDevice, byIP, total int, err error)
	// LiveSessionsForDevice lists the device's sessions that have not ended.
	LiveSessionsForDevice(ctx context.Context, device string) ([]Session, error)

	// IncrUsage adds n to the (day, kind, owner) counter and returns the total.
	IncrUsage(ctx context.Context, day, kind, owner string, n int) (int, error)
}

// NewID returns a random 128-bit hex identifier.
func NewID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
