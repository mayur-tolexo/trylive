package builder

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mayur-tolexo/trylive/internal/recipe"
	"github.com/mayur-tolexo/trylive/internal/repo"
	"github.com/mayur-tolexo/trylive/internal/sandbox"
	"github.com/mayur-tolexo/trylive/internal/store"
)

func TestParseListening(t *testing.T) {
	ss := "LISTEN 0 511 0.0.0.0:3000 0.0.0.0:*\nLISTEN 0 128 *:44772 *:*\nLISTEN 0 4096 [::]:5173 [::]:*\n"
	if got := ParseListening(ss); len(got) != 2 || got[0] != 3000 || got[1] != 5173 {
		t.Errorf("ss ports = %v", got)
	}
	proc := "  sl  local_address rem_address   st tx_queue\n   0: 00000000:0BB8 00000000:0000 0A 00000000:00000000\n   1: 0100007F:1F90 00000000:0000 01 00000000:00000000\n"
	if got := ParseListening(proc); len(got) != 1 || got[0] != 3000 {
		t.Errorf("proc ports = %v", got)
	}
	if ChoosePort(5173, []int{3000, 5173}) != 5173 || ChoosePort(9999, []int{3000, 5173}) != 3000 || ChoosePort(1, nil) != 0 {
		t.Error("ChoosePort preference wrong")
	}
}

// manifestJSON is what the fake inspector prints.
func manifestJSON(m recipe.Manifest) string {
	b, _ := json.Marshal(m)
	return string(b)
}

var info = repo.Info{Ref: repo.Ref{Owner: "octo", Name: "app"}, DefaultBranch: "main", SHA: "0123456789abcdef", CloneURL: "https://github.com/octo/app.git"}

// newEnv wires a builder over fakes; the inspector reports a Vite app.
func newEnv(t *testing.T) (*Builder, *sandbox.Fake, *store.Memory) {
	t.Helper()
	f := sandbox.NewFake()
	f.ExecScript["python3 inspect.py"] = sandbox.ExecResult{Stdout: manifestJSON(recipe.Manifest{
		PackageJSON: &recipe.PackageJSON{Scripts: map[string]string{"dev": "vite"}, DevDependencies: map[string]string{"vite": "5"}},
	})}
	f.ExecScript["sh -c ss"] = sandbox.ExecResult{Stdout: "LISTEN 0 511 0.0.0.0:5173 0.0.0.0:*\n"}
	st := store.NewMemory()
	b := &Builder{Sandbox: f, Store: st, Concurrency: 2}
	return b, f, st
}

// waitDeleted waits for the fake to record n deletions, which happen in a
// deferred call just after the build status is stored.
func waitDeleted(t *testing.T, f *sandbox.Fake, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.DeletedIDs()) == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("deleted = %v, want %d", f.DeletedIDs(), n)
}

// waitDone blocks until the build leaves the in-progress set.
func waitDone(t *testing.T, b *Builder, st *store.Memory, id string) store.Build {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		bld, _ := st.GetBuild(context.Background(), id)
		if bld.Status == store.BuildReady || bld.Status == store.BuildFailed || bld.Status == store.BuildUnsupported {
			return bld
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("build did not finish")
	return store.Build{}
}

func TestBuildHappyPath(t *testing.T) {
	b, f, st := newEnv(t)
	ctx := context.Background()
	bld, err := b.Ensure(ctx, info)
	if err != nil {
		t.Fatal(err)
	}
	// A second Ensure for the same commit shares the build.
	again, _ := b.Ensure(ctx, info)
	if again.ID != bld.ID {
		t.Error("duplicate build started for one commit")
	}

	events, _ := b.Subscribe(ctx, bld.ID)
	var lines []string
	for ev := range events {
		if ev.Type == "log" {
			lines = append(lines, ev.Phase+": "+ev.Line)
		}
	}
	done := waitDone(t, b, st, bld.ID)
	if done.Status != store.BuildReady || done.Kind != recipe.KindWeb || done.Port != 5173 || done.SnapshotID == "" || done.GoldenSandboxID == "" || done.BuiltAt == nil {
		t.Fatalf("build = %+v", done)
	}
	if done.Recipe == nil || done.Recipe.Detector != "package.json" || done.Recipe.Start != "npm run dev -- --host 0.0.0.0 --port 5173" {
		t.Errorf("recipe = %+v", done.Recipe)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"clone: git clone --depth 1 --branch main", "install: $ npm install --no-audit --no-fund", "start: $ npm run dev", "start: listening on port 5173", "snapshot: snapshot snap-"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q in:\n%s", want, joined)
		}
	}
	if !strings.Contains(done.Log, "[start] listening on port 5173") {
		t.Errorf("stored log = %q", done.Log)
	}
	calls := strings.Join(f.CallLog(), "\n")
	if !strings.Contains(calls, "egress=allow_list/true") || !strings.Contains(calls, "on_idle=pause") {
		t.Errorf("build sandbox spec wrong:\n%s", calls)
	}
	if !strings.Contains(calls, "exec npm install --no-audit --no-fund cwd=repo") || !strings.Contains(calls, "start npm run dev -- --host 0.0.0.0 --port 5173 cwd=repo") {
		t.Errorf("commands not run in repo dir:\n%s", calls)
	}
	if !strings.Contains(calls, "pause sb-1") {
		t.Errorf("golden not paused:\n%s", calls)
	}
	if d := f.DeletedIDs(); len(d) != 0 {
		t.Errorf("golden must not be deleted, deleted=%v", d)
	}
	// Subscribing after completion replays the stored log and closes.
	replay, _ := b.Subscribe(ctx, bld.ID)
	n := 0
	for ev := range replay {
		if ev.Type == "log" {
			n++
		}
	}
	if n == 0 {
		t.Error("no replay for a finished build")
	}
}

func TestBuildTerminalWhenNothingListens(t *testing.T) {
	b, f, st := newEnv(t)
	f.ExecScript["sh -c ss"] = sandbox.ExecResult{Stdout: "LISTEN 0 128 *:44772 *:*\n"}
	f.ExecScript["python3 inspect.py"] = sandbox.ExecResult{Stdout: manifestJSON(recipe.Manifest{GoMod: "module x"})}
	b.ProbeTimeout = 200 * time.Millisecond
	bld, _ := b.Ensure(context.Background(), info)
	done := waitDone(t, b, st, bld.ID)
	if done.Status != store.BuildReady || done.Kind != recipe.KindTerminal || done.Port != 0 {
		t.Fatalf("build = %+v", done)
	}
}

func TestBuildUnsupportedDeletesSandbox(t *testing.T) {
	b, f, st := newEnv(t)
	f.ExecScript["python3 inspect.py"] = sandbox.ExecResult{Stdout: manifestJSON(recipe.Manifest{Files: []string{"LICENSE"}})}
	bld, _ := b.Ensure(context.Background(), info)
	done := waitDone(t, b, st, bld.ID)
	if done.Status != store.BuildUnsupported || !strings.Contains(done.Error, "detect") {
		t.Fatalf("build = %+v", done)
	}
	waitDeleted(t, f, 1)
}

func TestBuildCloneFailure(t *testing.T) {
	b, f, st := newEnv(t)
	f.ExecScript["git clone"] = sandbox.ExecResult{ExitCode: 128, Stderr: "fatal: repository not found"}
	bld, _ := b.Ensure(context.Background(), info)
	done := waitDone(t, b, st, bld.ID)
	if done.Status != store.BuildFailed || !strings.Contains(done.Error, "clone") || !strings.Contains(done.Log, "repository not found") {
		t.Fatalf("build = %+v", done)
	}
}

// fakeLLM answers repair requests with one fixed command list.
type fakeLLM struct{ repair string }

func (f fakeLLM) Complete(_ context.Context, system, _ string) (string, error) {
	if strings.Contains(system, "fix a failing install") {
		return f.repair, nil
	}
	return `{"kind":"web","install":["pip install -r requirements.txt"],"start":"python3 app.py","port":8000}`, nil
}

func TestInstallRepairViaModel(t *testing.T) {
	b, f, st := newEnv(t)
	b.LLM = fakeLLM{repair: `["npm install --legacy-peer-deps"]`}
	f.ExecScript["npm install --no-audit"] = sandbox.ExecResult{ExitCode: 1, Stderr: "ERESOLVE could not resolve"}
	f.ExecScript["npm install --legacy-peer-deps"] = sandbox.ExecResult{ExitCode: 0}
	bld, _ := b.Ensure(context.Background(), info)
	done := waitDone(t, b, st, bld.ID)
	if done.Status != store.BuildReady {
		t.Fatalf("build = %+v", done)
	}
	if done.Recipe.Install[0] != "npm install --legacy-peer-deps" || !strings.Contains(done.Log, "asking the model for a fix (1/2)") {
		t.Errorf("repair not recorded: %+v\n%s", done.Recipe, done.Log)
	}
}

func TestInstallFailsWithoutModel(t *testing.T) {
	b, f, st := newEnv(t)
	f.ExecScript["npm install"] = sandbox.ExecResult{ExitCode: 1, Stderr: "boom"}
	bld, _ := b.Ensure(context.Background(), info)
	done := waitDone(t, b, st, bld.ID)
	if done.Status != store.BuildFailed || !strings.Contains(done.Error, "install") {
		t.Fatalf("build = %+v", done)
	}
	waitDeleted(t, f, 1)
}

func TestModelDetectionWhenNoDetectorMatches(t *testing.T) {
	b, f, st := newEnv(t)
	b.LLM = fakeLLM{}
	f.ExecScript["python3 inspect.py"] = sandbox.ExecResult{Stdout: manifestJSON(recipe.Manifest{Files: []string{"app.py", "README.md"}, Readme: "run python3 app.py"})}
	f.ExecScript["sh -c ss"] = sandbox.ExecResult{Stdout: "LISTEN 0 5 0.0.0.0:8000 0.0.0.0:*\n"}
	bld, _ := b.Ensure(context.Background(), info)
	done := waitDone(t, b, st, bld.ID)
	if done.Status != store.BuildReady || done.Recipe.Detector != "model" || done.Port != 8000 {
		t.Fatalf("build = %+v recipe=%+v", done, done.Recipe)
	}
}

func TestSnapshotFailure(t *testing.T) {
	b, f, st := newEnv(t)
	f.SnapshotFails = true
	bld, _ := b.Ensure(context.Background(), info)
	done := waitDone(t, b, st, bld.ID)
	if done.Status != store.BuildFailed || !strings.Contains(done.Error, "snapshot") {
		t.Fatalf("build = %+v", done)
	}
	waitDeleted(t, f, 1)
}

func TestEnsureRestartsStaleBuilding(t *testing.T) {
	b, _, st := newEnv(t)
	stale := &store.Build{Owner: "octo", Repo: "app", SHA: info.SHA, Status: store.BuildBuilding}
	st.CreateBuild(context.Background(), stale)
	bld, err := b.Ensure(context.Background(), info)
	if err != nil || bld.ID != stale.ID {
		t.Fatalf("Ensure = %+v, %v", bld, err)
	}
	if done := waitDone(t, b, st, bld.ID); done.Status != store.BuildReady {
		t.Errorf("stale build not restarted: %+v", done)
	}
}

func TestEnsureCreateBusy(t *testing.T) {
	b, f, st := newEnv(t)
	f.CreateErr = sandbox.ErrBusy
	bld, _ := b.Ensure(context.Background(), info)
	done := waitDone(t, b, st, bld.ID)
	if done.Status != store.BuildFailed || !errors.Is(sandbox.ErrBusy, sandbox.ErrBusy) || !strings.Contains(done.Error, "busy") {
		t.Fatalf("build = %+v", done)
	}
}
