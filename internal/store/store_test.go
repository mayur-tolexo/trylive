package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mayur-tolexo/trylive/internal/recipe"
)

// runConformance holds both implementations to the same behaviour.
func runConformance(t *testing.T, s Store) {
	ctx := context.Background()
	owner, repoName := "o-"+NewID()[:6], "r"

	b := &Build{Owner: owner, Repo: repoName, SHA: "aaa", Status: BuildQueued}
	if err := s.CreateBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBuildBySHA(ctx, owner, repoName, "aaa")
	if err != nil || got.ID != b.ID || got.Status != BuildQueued || got.Recipe != nil {
		t.Fatalf("GetBuildBySHA = %+v, %v", got, err)
	}
	if _, err := s.GetBuildBySHA(ctx, owner, repoName, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing sha err = %v", err)
	}

	now := time.Now().Truncate(time.Microsecond)
	b.Status, b.Kind, b.Port, b.SnapshotID, b.GoldenSandboxID, b.Log, b.BuiltAt = BuildReady, recipe.KindWeb, 3000, "snap", "golden", "cloned\ninstalled\n", &now
	b.Recipe = &recipe.Recipe{Kind: recipe.KindWeb, Install: []string{"npm install"}, Start: "npm run dev", Port: 3000, Env: map[string]string{"PORT": "3000"}, Detector: "package.json"}
	if err := s.UpdateBuild(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetBuild(ctx, b.ID)
	if got.Status != BuildReady || got.Recipe == nil || got.Recipe.Start != "npm run dev" || got.Recipe.Env["PORT"] != "3000" || got.BuiltAt == nil || !got.BuiltAt.Equal(now) || got.Log != "cloned\ninstalled\n" {
		t.Errorf("updated build = %+v", got)
	}
	// Mutating the returned recipe must not leak into the store.
	got.Recipe.Start = "hacked"
	again, _ := s.GetBuild(ctx, b.ID)
	if again.Recipe.Start != "npm run dev" {
		t.Error("stored recipe mutated through a returned copy")
	}

	time.Sleep(2 * time.Millisecond)
	b2 := &Build{Owner: owner, Repo: repoName, SHA: "bbb", Status: BuildBuilding}
	s.CreateBuild(ctx, b2)
	latest, err := s.LatestBuild(ctx, owner, repoName)
	if err != nil || latest.ID != b2.ID {
		t.Errorf("LatestBuild = %+v, %v", latest, err)
	}
	if _, err := s.LatestBuild(ctx, "nobody", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("LatestBuild missing err = %v", err)
	}
	if err := s.RecordVisit(ctx, b.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.RecordVisit(ctx, b.ID, now.Add(2*time.Hour))
	got, _ = s.GetBuild(ctx, b.ID)
	if got.Visits != 2 || !got.LastVisitAt.Equal(now.Add(2*time.Hour)) {
		t.Errorf("visits = %d last=%v", got.Visits, got.LastVisitAt)
	}
	if err := s.UpdateBuild(ctx, &Build{ID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateBuild missing err = %v", err)
	}

	dev, ip := "dev-"+NewID()[:6], "ip-"+NewID()[:6]
	sess := &Session{BuildID: b.ID, Device: dev, IP: ip, Status: SessionPending}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	exp := now.Add(15 * time.Minute)
	sess.Status, sess.SandboxID, sess.PreviewURL, sess.TerminalReady, sess.ExpiresAt = SessionLive, "sb", "https://p", true, &exp
	if err := s.UpdateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	gs, err := s.GetSession(ctx, sess.ID)
	if err != nil || gs.Status != SessionLive || gs.PreviewURL != "https://p" || !gs.TerminalReady || gs.ExpiresAt == nil || !gs.ExpiresAt.Equal(exp) {
		t.Fatalf("GetSession = %+v, %v", gs, err)
	}
	other := &Session{BuildID: b.ID, Device: "other", IP: ip, Status: SessionBuilding}
	s.CreateSession(ctx, other)
	ended := &Session{BuildID: b.ID, Device: dev, IP: ip, Status: SessionEnded}
	s.CreateSession(ctx, ended)
	byDev, byIP, total, err := s.LiveSessions(ctx, dev, ip)
	if err != nil || byDev != 1 || byIP != 2 || total < 2 {
		t.Errorf("LiveSessions = %d %d %d %v", byDev, byIP, total, err)
	}
	if _, err := s.GetSession(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSession missing err = %v", err)
	}

	key := "k-" + NewID()[:6]
	if n, _ := s.IncrUsage(ctx, "2026-09-06", "sessions", key, 1); n != 1 {
		t.Errorf("first incr = %d", n)
	}
	if n, _ := s.IncrUsage(ctx, "2026-09-06", "sessions", key, 2); n != 3 {
		t.Errorf("second incr = %d", n)
	}
}

func TestMemoryConformance(t *testing.T) { runConformance(t, NewMemory()) }

func TestPostgresConformance(t *testing.T) {
	dsn := os.Getenv("TRYLIVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TRYLIVE_TEST_DATABASE_URL not set")
	}
	p, err := OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	runConformance(t, p)
}
