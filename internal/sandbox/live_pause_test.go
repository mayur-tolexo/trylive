package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLivePauseKeepsSnapshotRestorable checks the golden model: a paused
// sandbox still owns its snapshot, and that snapshot still restores. Needs
// the NEEV_* variables plus TRYLIVE_LIVE_GOLDEN=<sandbox id of a Ready golden
// with a snapshot>; skipped otherwise.
func TestLivePauseKeepsSnapshotRestorable(t *testing.T) {
	c := liveClient(t)
	goldenID := os.Getenv("TRYLIVE_LIVE_GOLDEN")
	snapID := os.Getenv("TRYLIVE_LIVE_SNAPSHOT")
	if goldenID == "" || snapID == "" {
		t.Skip("TRYLIVE_LIVE_GOLDEN and TRYLIVE_LIVE_SNAPSHOT not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := c.Pause(ctx, goldenID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	var sb Sandbox
	for i := 0; i < 60; i++ {
		var err error
		sb, err = c.Get(ctx, goldenID)
		if err != nil {
			t.Fatal(err)
		}
		if sb.Phase == PhasePaused {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("golden phase after pause: %s", sb.Phase)
	if sb.Phase != PhasePaused {
		t.Fatalf("golden did not pause: %s", sb.Phase)
	}
	snap, err := c.GetSnapshot(ctx, snapID)
	if err != nil || snap.Status != SnapshotReady {
		t.Fatalf("snapshot after pause = %+v, %v", snap, err)
	}

	t0 := time.Now()
	v, err := c.Create(ctx, CreateSpec{Name: "tl-pause-check", Restore: snapID, Resources: &testSize, Egress: Egress{Mode: "deny_all"},
		Lifecycle: Lifecycle{IdleTimeoutSeconds: 300, MaxLifetimeSeconds: 600, OnIdle: "delete"}})
	if err != nil {
		t.Fatalf("restore from paused golden's snapshot: %v", err)
	}
	defer c.Delete(context.Background(), v.ID)
	if v, err = c.WaitReady(ctx, v.ID, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	res, err := c.Exec(ctx, v, ExecSpec{Command: "sh", Args: []string{"-c", "ss -ltnH 2>/dev/null || cat /proc/net/tcp /proc/net/tcp6 2>/dev/null"}, TimeoutMS: 5000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("restored in %s; listening: %s", time.Since(t0).Round(time.Millisecond), strings.TrimSpace(res.Stdout))
}
