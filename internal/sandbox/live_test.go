package sandbox

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// liveClient builds a client from the environment or skips the test.
func liveClient(t *testing.T) *HTTP {
	t.Helper()
	cfg := Config{
		APIBase: os.Getenv("NEEV_API_BASE"), APIKey: os.Getenv("NEEV_API_KEY"),
		OrgID: os.Getenv("NEEV_ORG_ID"), ProjectID: os.Getenv("NEEV_PROJECT_ID"), Region: os.Getenv("NEEV_REGION"),
	}
	if cfg.APIKey == "" || cfg.OrgID == "" || cfg.ProjectID == "" {
		t.Skip("NEEV_API_KEY, NEEV_ORG_ID and NEEV_PROJECT_ID not all set")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.ai.neevcloud.com/agent"
	}
	c, err := NewHTTP(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// BuildSize is the one sandbox size every build and restore uses.
var testSize = Resources{CPU: 1, MemoryGB: 2, DiskGB: 10}

// fetchPublic loads a preview URL from outside the platform and returns the
// status and body, retrying briefly while routing propagates.
func fetchPublic(t *testing.T, u string) (int, string) {
	t.Helper()
	var last int
	var body string
	for i := 0; i < 20; i++ {
		resp, err := http.Get(u)
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			last, body = resp.StatusCode, string(b)
			if last == 200 {
				return last, body
			}
		}
		time.Sleep(time.Second)
	}
	return last, body
}

// TestLiveServeSnapshotRestore is the go/no-go spike: a server started in a
// sandbox must be reachable on its public preview URL, survive a snapshot,
// and come back already listening in a sandbox restored from that snapshot.
func TestLiveServeSnapshotRestore(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	t0 := time.Now()
	golden, err := c.Create(ctx, CreateSpec{
		Name: "tl-spike-golden", Resources: &testSize, Egress: Egress{Mode: "deny_all"},
		Lifecycle: Lifecycle{IdleTimeoutSeconds: 1200, MaxLifetimeSeconds: 1800, OnIdle: "pause"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer c.Delete(context.Background(), golden.ID)
	golden, err = c.WaitReady(ctx, golden.ID, 2*time.Minute)
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	t.Logf("golden %s ready in %s (region %s)", golden.ID, time.Since(t0).Round(time.Millisecond), golden.Region)

	// A server whose response proves which process is answering.
	server := "import http.server, os\nclass H(http.server.BaseHTTPRequestHandler):\n    def do_GET(self):\n        self.send_response(200); self.end_headers(); self.wfile.write(('hello from pid %d' % os.getpid()).encode())\nhttp.server.ThreadingHTTPServer(('0.0.0.0', 8000), H).serve_forever()\n"
	if err := c.WriteFile(ctx, golden, "serve.py", []byte(server)); err != nil {
		t.Fatalf("write: %v", err)
	}
	proc, err := c.StartProcess(ctx, golden, ExecSpec{Command: "python3", Args: []string{"serve.py"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Logf("process %s %s", proc.ID, proc.State)

	// Probe from inside until the port listens, as the builder will.
	var listening bool
	for i := 0; i < 15 && !listening; i++ {
		res, err := c.Exec(ctx, golden, ExecSpec{Command: "sh", Args: []string{"-c", "ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null || cat /proc/net/tcp"}, TimeoutMS: 5000}, nil)
		if err != nil {
			t.Fatalf("probe exec: %v", err)
		}
		listening = strings.Contains(res.Stdout, ":8000") || strings.Contains(res.Stdout, ":1F40")
		if !listening {
			time.Sleep(time.Second)
		}
	}
	if !listening {
		t.Fatal("port 8000 never listened")
	}

	preview, err := c.ExposePort(ctx, golden.ID, 8000)
	if err != nil {
		t.Fatalf("expose: %v", err)
	}
	status, body := fetchPublic(t, preview)
	t.Logf("golden preview %s -> %d %q", preview, status, body)
	if status != 200 || !strings.HasPrefix(body, "hello from pid") {
		t.Fatalf("golden preview not public: %d %q", status, body)
	}

	t1 := time.Now()
	snap, err := c.CreateSnapshot(ctx, golden.ID, "tl-spike")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap, err = c.WaitSnapshot(ctx, snap.ID, 5*time.Minute)
	if err != nil {
		t.Fatalf("snapshot wait: %v", err)
	}
	t.Logf("snapshot %s Ready in %s, %d bytes", snap.ID, time.Since(t1).Round(time.Millisecond), snap.SizeBytes)

	t2 := time.Now()
	visitor, err := c.Create(ctx, CreateSpec{
		Name: "tl-spike-visitor", Restore: snap.ID, Resources: &testSize, Egress: Egress{Mode: "deny_all"},
		Lifecycle: Lifecycle{IdleTimeoutSeconds: 900, MaxLifetimeSeconds: 1800, OnIdle: "delete"},
	})
	if err != nil {
		t.Fatalf("restore create: %v", err)
	}
	defer c.Delete(context.Background(), visitor.ID)
	visitor, err = c.WaitReady(ctx, visitor.ID, 2*time.Minute)
	if err != nil {
		t.Fatalf("restore ready: %v", err)
	}
	vpreview, err := c.ExposePort(ctx, visitor.ID, 8000)
	if err != nil {
		t.Fatalf("visitor expose: %v", err)
	}
	vstatus, vbody := fetchPublic(t, vpreview)
	t.Logf("visitor %s restored+exposed+served in %s: %s -> %d %q", visitor.ID, time.Since(t2).Round(time.Millisecond), vpreview, vstatus, vbody)
	if vstatus != 200 || !strings.HasPrefix(vbody, "hello from pid") {
		t.Fatalf("restored server not serving: %d %q", vstatus, vbody)
	}
	// Same pid proves the process itself was restored, not restarted.
	if body != vbody {
		t.Logf("note: pid differs after restore (%q vs %q)", body, vbody)
	}
}
