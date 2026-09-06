package recipe

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// detectTryliveYAML honours a maintainer-provided recipe verbatim (after
// validation), so a repo can opt out of guessing entirely.
func detectTryliveYAML(m Manifest) (Recipe, bool) {
	if strings.TrimSpace(m.TryliveYAML) == "" {
		return Recipe{}, false
	}
	var r Recipe
	if err := yaml.Unmarshal([]byte(m.TryliveYAML), &r); err != nil {
		return Recipe{}, false
	}
	if r.Kind == "" {
		if r.Start != "" {
			r.Kind = KindWeb
		} else {
			r.Kind = KindTerminal
		}
	}
	if r.Kind == KindWeb && r.Port == 0 {
		r.Port = 3000
	}
	return r, true
}

// nodeFramework maps a dependency to the dev-server port it listens on and
// whether it is Vite-based (needs --host to bind beyond loopback).
type nodeFramework struct {
	dep  string
	port int
	vite bool
}

// Ordered so meta-frameworks win over the bundler they wrap.
var nodeFrameworks = []nodeFramework{
	{"next", 3000, false}, {"nuxt", 3000, false}, {"@remix-run/dev", 3000, false}, {"gatsby", 8000, false},
	{"astro", 4321, true}, {"@sveltejs/kit", 5173, true}, {"@angular/cli", 4200, false},
	{"react-scripts", 3000, false}, {"@vue/cli-service", 8080, false}, {"vite", 5173, true},
	{"express", 3000, false}, {"fastify", 3000, false}, {"koa", 3000, false}, {"hono", 3000, false},
	{"@nestjs/core", 3000, false}, {"http-server", 8080, false}, {"serve", 3000, false},
}

// detectNode handles package.json projects: package manager from the
// lockfile, the dev script when present, and a port from the framework.
func detectNode(m Manifest) (Recipe, bool) {
	p := m.PackageJSON
	if p == nil {
		return Recipe{}, false
	}
	pm := packageManager(m.Lockfiles, p.PackageManager)
	r := Recipe{Kind: KindTerminal, Install: []string{pm.install}, Env: map[string]string{"HOST": "0.0.0.0", "CI": "true"}}

	var fw *nodeFramework
	for i := range nodeFrameworks {
		if _, ok := p.Dependencies[nodeFrameworks[i].dep]; ok {
			fw = &nodeFrameworks[i]
			break
		}
		if _, ok := p.DevDependencies[nodeFrameworks[i].dep]; ok {
			fw = &nodeFrameworks[i]
			break
		}
	}
	script := ""
	for _, s := range []string{"dev", "start", "serve", "preview"} {
		if _, ok := p.Scripts[s]; ok {
			script = s
			break
		}
	}
	if script == "" {
		return r, true
	}
	r.Kind = KindWeb
	r.Port = 3000
	if fw != nil {
		r.Port = fw.port
	}
	r.Env["PORT"] = fmt.Sprint(r.Port)
	r.Start = pm.run(script)
	// Vite-family dev servers bind loopback by default; the preview gateway
	// reaches the pod IP, so they must be told to listen everywhere.
	if fw != nil && fw.vite {
		r.Start += " " + pm.passthrough + "--host 0.0.0.0 --port " + fmt.Sprint(r.Port)
	}
	return r, true
}

// pkgManager knows how to install and run scripts for one Node package
// manager. Everything runs through npx so the sandbox image only needs Node.
type pkgManager struct {
	install     string
	runPrefix   string
	passthrough string // separator before args forwarded to the script
}

func (p pkgManager) run(script string) string { return p.runPrefix + " " + script }

// packageManager picks by lockfile, then packageManager field, else npm.
func packageManager(lockfiles []string, field string) pkgManager {
	has := func(name string) bool {
		for _, l := range lockfiles {
			if l == name {
				return true
			}
		}
		return false
	}
	switch {
	case has("pnpm-lock.yaml") || strings.HasPrefix(field, "pnpm"):
		return pkgManager{"npx -y pnpm install", "npx -y pnpm run", ""}
	case has("yarn.lock") || strings.HasPrefix(field, "yarn"):
		return pkgManager{"npx -y yarn install", "npx -y yarn run", ""}
	case has("bun.lockb") || has("bun.lock") || strings.HasPrefix(field, "bun"):
		return pkgManager{"npx -y bun install", "npx -y bun run", "-- "}
	default:
		return pkgManager{"npm install --no-audit --no-fund", "npm run", "-- "}
	}
}

// pyFramework maps an import name to how it is started.
type pyFramework struct {
	dep   string
	port  int
	start func(entry string) string
	env   map[string]string
}

var pyFrameworks = []pyFramework{
	{"streamlit", 8501, func(e string) string {
		return "streamlit run " + e + " --server.address 0.0.0.0 --server.port 8501 --server.headless true"
	}, nil},
	{"gradio", 7860, func(e string) string { return "python3 " + e }, map[string]string{"GRADIO_SERVER_NAME": "0.0.0.0", "GRADIO_SERVER_PORT": "7860"}},
	// The preview host is unknown to the project; these are the env names the
	// common Django templates read for ALLOWED_HOSTS.
	{"django", 8000, func(e string) string { return "python3 manage.py runserver 0.0.0.0:8000" }, map[string]string{"ALLOWED_HOSTS": "*", "DJANGO_ALLOWED_HOSTS": "*"}},
	{"fastapi", 8000, func(e string) string { return "uvicorn " + module(e) + ":app --host 0.0.0.0 --port 8000" }, nil},
	{"flask", 5000, func(e string) string { return "flask --app " + module(e) + " run --host 0.0.0.0 --port 5000" }, nil},
}

// detectPyProject and detectRequirements share one body: the dependency
// text differs, the install command differs, the frameworks do not.
func detectPyProject(m Manifest) (Recipe, bool) {
	if strings.TrimSpace(m.PyProject) == "" {
		return Recipe{}, false
	}
	return pythonRecipe(m, m.PyProject, "pip install -e .")
}

func detectRequirements(m Manifest) (Recipe, bool) {
	if strings.TrimSpace(m.Requirements) == "" {
		return Recipe{}, false
	}
	return pythonRecipe(m, m.Requirements, "pip install -r requirements.txt")
}

// pythonRecipe installs, then starts the first recognised framework using
// the most likely entry file; with no framework the visitor gets a shell.
func pythonRecipe(m Manifest, deps, install string) (Recipe, bool) {
	r := Recipe{Kind: KindTerminal, Install: []string{install}, Env: map[string]string{"PYTHONUNBUFFERED": "1"}}
	lower := strings.ToLower(deps)
	for _, fw := range pyFrameworks {
		if !regexp.MustCompile(`(?m)(^|["'\s\[,])` + fw.dep + `([\s\[<>=~!;"',]|$)`).MatchString(lower) {
			continue
		}
		entry := pyEntry(m, fw.dep)
		if entry == "" && fw.dep != "django" {
			continue
		}
		r.Kind, r.Port, r.Start = KindWeb, fw.port, fw.start(entry)
		for k, v := range fw.env {
			r.Env[k] = v
		}
		if fw.dep == "django" {
			r.Install = append(r.Install, "python3 manage.py migrate --noinput")
		}
		break
	}
	return r, true
}

// pyEntry picks the entry file for a framework from the inspector's list of
// candidate Python files at the repo root.
func pyEntry(m Manifest, dep string) string {
	prefs := map[string][]string{
		"streamlit": {"streamlit_app.py", "app.py", "main.py", "Home.py"},
		"gradio":    {"app.py", "demo.py", "main.py"},
		"fastapi":   {"main.py", "app.py", "server.py", "api.py"},
		"flask":     {"app.py", "main.py", "server.py", "wsgi.py", "run.py"},
		"django":    {"manage.py"},
	}
	for _, want := range prefs[dep] {
		for _, f := range m.Files {
			if f == want {
				return f
			}
		}
	}
	return ""
}

// module turns app.py into app for uvicorn/flask.
func module(entry string) string { return strings.TrimSuffix(entry, ".py") }

// detectGo runs the module's main package; the probe decides whether a port
// appears.
func detectGo(m Manifest) (Recipe, bool) {
	if strings.TrimSpace(m.GoMod) == "" {
		return Recipe{}, false
	}
	return Recipe{Kind: KindWeb, Install: []string{"go mod download"}, Start: "go run .", Port: 8080, Env: map[string]string{"PORT": "8080", "HOST": "0.0.0.0"}}, true
}

func detectCargo(m Manifest) (Recipe, bool) {
	if strings.TrimSpace(m.CargoToml) == "" {
		return Recipe{}, false
	}
	return Recipe{Kind: KindWeb, Install: []string{"cargo build"}, Start: "cargo run", Port: 8080, Env: map[string]string{"PORT": "8080", "HOST": "0.0.0.0"}}, true
}

var procfileWeb = regexp.MustCompile(`(?m)^web:\s*(.+)$`)

// detectProcfile uses the web process; $PORT is supplied as 8080.
func detectProcfile(m Manifest) (Recipe, bool) {
	match := procfileWeb.FindStringSubmatch(m.Procfile)
	if match == nil {
		return Recipe{}, false
	}
	start := strings.ReplaceAll(strings.TrimSpace(match[1]), "$PORT", "8080")
	if validateCommand(start) != nil {
		return Recipe{}, false
	}
	return Recipe{Kind: KindWeb, Start: start, Port: 8080, Env: map[string]string{"PORT": "8080", "HOST": "0.0.0.0"}}, true
}

// detectMakefile picks the first conventional serve target.
func detectMakefile(m Manifest) (Recipe, bool) {
	for _, t := range []string{"dev", "serve", "run", "start"} {
		for _, have := range m.MakeTargets {
			if have == t {
				return Recipe{Kind: KindWeb, Start: "make " + t, Port: 8080, Env: map[string]string{"PORT": "8080", "HOST": "0.0.0.0"}}, true
			}
		}
	}
	return Recipe{}, false
}

// detectStatic serves a plain site with Python's built-in server.
func detectStatic(m Manifest) (Recipe, bool) {
	if !m.HasIndexHTML {
		return Recipe{}, false
	}
	return Recipe{Kind: KindWeb, Start: "python3 -m http.server 8080 --bind 0.0.0.0", Port: 8080}, true
}
