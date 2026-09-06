package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mayur-tolexo/trylive/internal/recipe"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Postgres is the production Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres connects, applies pending migrations, and returns the store.
func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	p := &Postgres{pool: pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

// Close releases the pool.
func (p *Postgres) Close() { p.pool.Close() }

// migrate applies each embedded migration once, in name order, each inside a
// transaction so a failure leaves the previous schema intact.
func (p *Postgres) migrate(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var applied bool
		if err := p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := p.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

const buildColumns = `id, owner, repo, sha, status, kind, recipe, port, golden_sandbox_id, snapshot_id, log, error, built_at, last_visit_at, visits, created_at, updated_at`

// recipeJSON encodes the optional recipe for the JSONB column.
func recipeJSON(r *recipe.Recipe) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return json.Marshal(r)
}

func (p *Postgres) CreateBuild(ctx context.Context, b *Build) error {
	if b.ID == "" {
		b.ID = NewID()
	}
	now := time.Now()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	if b.LastVisitAt.IsZero() {
		b.LastVisitAt = now
	}
	b.UpdatedAt = now
	rj, err := recipeJSON(b.Recipe)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO builds (`+buildColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		b.ID, b.Owner, b.Repo, b.SHA, b.Status, b.Kind, rj, b.Port, b.GoldenSandboxID, b.SnapshotID, b.Log, b.Error, b.BuiltAt, b.LastVisitAt, b.Visits, b.CreatedAt, b.UpdatedAt)
	return err
}

func (p *Postgres) UpdateBuild(ctx context.Context, b *Build) error {
	b.UpdatedAt = time.Now()
	rj, err := recipeJSON(b.Recipe)
	if err != nil {
		return err
	}
	tag, err := p.pool.Exec(ctx, `UPDATE builds SET status=$2, kind=$3, recipe=$4, port=$5, golden_sandbox_id=$6, snapshot_id=$7, log=$8, error=$9, built_at=$10, updated_at=$11 WHERE id=$1`,
		b.ID, b.Status, b.Kind, rj, b.Port, b.GoldenSandboxID, b.SnapshotID, b.Log, b.Error, b.BuiltAt, b.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// scanBuild reads one row in buildColumns order.
func scanBuild(row pgx.Row) (Build, error) {
	var b Build
	var rj []byte
	err := row.Scan(&b.ID, &b.Owner, &b.Repo, &b.SHA, &b.Status, &b.Kind, &rj, &b.Port, &b.GoldenSandboxID, &b.SnapshotID, &b.Log, &b.Error, &b.BuiltAt, &b.LastVisitAt, &b.Visits, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Build{}, ErrNotFound
	}
	if err != nil {
		return Build{}, err
	}
	if len(rj) > 0 {
		var r recipe.Recipe
		if err := json.Unmarshal(rj, &r); err != nil {
			return Build{}, err
		}
		b.Recipe = &r
	}
	return b, nil
}

func (p *Postgres) GetBuild(ctx context.Context, id string) (Build, error) {
	return scanBuild(p.pool.QueryRow(ctx, `SELECT `+buildColumns+` FROM builds WHERE id = $1`, id))
}

func (p *Postgres) GetBuildBySHA(ctx context.Context, owner, repo, sha string) (Build, error) {
	return scanBuild(p.pool.QueryRow(ctx, `SELECT `+buildColumns+` FROM builds WHERE owner = $1 AND repo = $2 AND sha = $3`, owner, repo, sha))
}

func (p *Postgres) LatestBuild(ctx context.Context, owner, repo string) (Build, error) {
	return scanBuild(p.pool.QueryRow(ctx, `SELECT `+buildColumns+` FROM builds WHERE owner = $1 AND repo = $2 ORDER BY created_at DESC LIMIT 1`, owner, repo))
}

func (p *Postgres) RecordVisit(ctx context.Context, buildID string, at time.Time) error {
	tag, err := p.pool.Exec(ctx, `UPDATE builds SET visits = visits + 1, last_visit_at = $2 WHERE id = $1`, buildID, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const sessionColumns = `id, build_id, device, ip, sandbox_id, preview_url, status, terminal_ready, extended, created_at, expires_at, ended_reason`

func (p *Postgres) CreateSession(ctx context.Context, s *Session) error {
	if s.ID == "" {
		s.ID = NewID()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO sessions (`+sessionColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		s.ID, s.BuildID, s.Device, s.IP, s.SandboxID, s.PreviewURL, s.Status, s.TerminalReady, s.Extended, s.CreatedAt, s.ExpiresAt, s.EndedReason)
	return err
}

func (p *Postgres) UpdateSession(ctx context.Context, s *Session) error {
	tag, err := p.pool.Exec(ctx, `UPDATE sessions SET sandbox_id=$2, preview_url=$3, status=$4, terminal_ready=$5, extended=$6, expires_at=$7, ended_reason=$8 WHERE id=$1`,
		s.ID, s.SandboxID, s.PreviewURL, s.Status, s.TerminalReady, s.Extended, s.ExpiresAt, s.EndedReason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) GetSession(ctx context.Context, id string) (Session, error) {
	var s Session
	err := p.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = $1`, id).
		Scan(&s.ID, &s.BuildID, &s.Device, &s.IP, &s.SandboxID, &s.PreviewURL, &s.Status, &s.TerminalReady, &s.Extended, &s.CreatedAt, &s.ExpiresAt, &s.EndedReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return s, err
}

func (p *Postgres) LiveSessions(ctx context.Context, device, ip string) (int, int, int, error) {
	var byDev, byIP, total int
	err := p.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE device = $1), count(*) FILTER (WHERE ip = $2), count(*)
		FROM sessions WHERE status <> 'ended'`, device, ip).Scan(&byDev, &byIP, &total)
	return byDev, byIP, total, err
}

func (p *Postgres) IncrUsage(ctx context.Context, day, kind, owner string, n int) (int, error) {
	var total int
	err := p.pool.QueryRow(ctx, `INSERT INTO usage_daily (day, kind, owner, count) VALUES ($1, $2, $3, $4)
		ON CONFLICT (day, kind, owner) DO UPDATE SET count = usage_daily.count + EXCLUDED.count RETURNING count`, day, kind, owner, n).Scan(&total)
	return total, err
}
