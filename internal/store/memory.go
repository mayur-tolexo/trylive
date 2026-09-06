package store

import (
	"context"
	"sync"
	"time"
)

// Memory is an in-process Store; data is lost on restart.
type Memory struct {
	mu       sync.Mutex
	builds   map[string]Build
	sessions map[string]Session
	usage    map[string]int
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{builds: map[string]Build{}, sessions: map[string]Session{}, usage: map[string]int{}}
}

func (m *Memory) CreateBuild(_ context.Context, b *Build) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.ID == "" {
		b.ID = NewID()
	}
	now := time.Now()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	m.builds[b.ID] = cloneBuild(*b)
	return nil
}

func (m *Memory) UpdateBuild(_ context.Context, b *Build) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.builds[b.ID]
	if !ok {
		return ErrNotFound
	}
	b.UpdatedAt = time.Now()
	// Visit counters belong to RecordVisit; a builder's stale copy must not
	// reset them (Postgres updates only the build fields for the same reason).
	b.Visits, b.LastVisitAt, b.CreatedAt = cur.Visits, cur.LastVisitAt, cur.CreatedAt
	m.builds[b.ID] = cloneBuild(*b)
	return nil
}

func (m *Memory) GetBuild(_ context.Context, id string) (Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.builds[id]
	if !ok {
		return Build{}, ErrNotFound
	}
	return cloneBuild(b), nil
}

func (m *Memory) GetBuildBySHA(_ context.Context, owner, repo, sha string) (Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.builds {
		if b.Owner == owner && b.Repo == repo && b.SHA == sha {
			return cloneBuild(b), nil
		}
	}
	return Build{}, ErrNotFound
}

func (m *Memory) LatestBuild(_ context.Context, owner, repo string) (Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Build
	for _, b := range m.builds {
		if b.Owner == owner && b.Repo == repo && (best == nil || b.CreatedAt.After(best.CreatedAt)) {
			bb := b
			best = &bb
		}
	}
	if best == nil {
		return Build{}, ErrNotFound
	}
	return cloneBuild(*best), nil
}

func (m *Memory) RecordVisit(_ context.Context, buildID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.builds[buildID]
	if !ok {
		return ErrNotFound
	}
	b.Visits++
	b.LastVisitAt = at
	m.builds[buildID] = b
	return nil
}

func (m *Memory) CreateSession(_ context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.ID == "" {
		s.ID = NewID()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	m.sessions[s.ID] = *s
	return nil
}

func (m *Memory) UpdateSession(_ context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[s.ID]; !ok {
		return ErrNotFound
	}
	m.sessions[s.ID] = *s
	return nil
}

func (m *Memory) GetSession(_ context.Context, id string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	return s, nil
}

func (m *Memory) LiveSessions(_ context.Context, device, ip string) (int, int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var byDev, byIP, total int
	for _, s := range m.sessions {
		if !s.Live() {
			continue
		}
		total++
		if s.Device == device {
			byDev++
		}
		if s.IP == ip {
			byIP++
		}
	}
	return byDev, byIP, total, nil
}

func (m *Memory) IncrUsage(_ context.Context, day, kind, owner string, n int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := day + "|" + kind + "|" + owner
	m.usage[k] += n
	return m.usage[k], nil
}

// cloneBuild copies the recipe pointer's target so callers cannot mutate
// stored state through it.
func cloneBuild(b Build) Build {
	if b.Recipe != nil {
		r := *b.Recipe
		b.Recipe = &r
	}
	return b
}
