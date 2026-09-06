// Package recipe decides how to install and run a repository. Detectors read
// a Manifest (what the inspector found in the cloned tree) and produce a
// Recipe; a model fills in when no detector is confident; Validate keeps
// every command inside an allow-list before it runs in a sandbox.
package recipe

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Kinds of result a build can reach.
const (
	KindWeb      = "web"      // a port listens after start
	KindTerminal = "terminal" // installed, but nothing to serve; visitor gets a shell
)

// Recipe is everything needed to install and start a repo.
type Recipe struct {
	Kind     string            `json:"kind" yaml:"kind"`
	Cwd      string            `json:"cwd,omitempty" yaml:"cwd"`
	Install  []string          `json:"install" yaml:"install"`
	Start    string            `json:"start,omitempty" yaml:"start"`
	Port     int               `json:"port,omitempty" yaml:"port"`
	Env      map[string]string `json:"env,omitempty" yaml:"env"`
	Detector string            `json:"detector" yaml:"-"`
}

// Manifest is what the inspector reports about a cloned repository: which
// well-known files exist and the contents of the small ones detectors read.
type Manifest struct {
	Files        []string     `json:"files"`    // top-level entries
	CmdDirs      []string     `json:"cmd_dirs"` // subdirectories of cmd/, for Go main packages
	PackageJSON  *PackageJSON `json:"package_json,omitempty"`
	Lockfiles    []string     `json:"lockfiles"`
	PyProject    string       `json:"pyproject,omitempty"`
	Requirements string       `json:"requirements,omitempty"`
	GoMod        string       `json:"go_mod,omitempty"`
	CargoToml    string       `json:"cargo_toml,omitempty"`
	Procfile     string       `json:"procfile,omitempty"`
	MakeTargets  []string     `json:"make_targets"`
	Readme       string       `json:"readme,omitempty"` // first ~6 KB
	TryliveYAML  string       `json:"trylive_yaml,omitempty"`
	Dockerfile   string       `json:"dockerfile,omitempty"`
	HasIndexHTML bool         `json:"has_index_html"`
}

// PackageJSON is the subset of package.json detectors use.
type PackageJSON struct {
	Name            string            `json:"name"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	PackageManager  string            `json:"packageManager"`
}

// ErrNoMatch means no detector recognised the repository.
var ErrNoMatch = errors.New("recipe: no detector matched")

// Detect runs the detectors in priority order and returns the first recipe.
// A trylive.yaml always wins; after that, the most specific ecosystem file.
func Detect(m Manifest) (Recipe, error) {
	for _, d := range detectors {
		r, ok := d.fn(m)
		if !ok {
			continue
		}
		r.Detector = d.name
		if err := Validate(r); err != nil {
			return Recipe{}, fmt.Errorf("detector %s produced an invalid recipe: %w", d.name, err)
		}
		return r, nil
	}
	return Recipe{}, ErrNoMatch
}

type detector struct {
	name string
	fn   func(Manifest) (Recipe, bool)
}

// detectors are ordered from most to least specific.
var detectors = []detector{
	{"trylive.yaml", detectTryliveYAML},
	{"package.json", detectNode},
	{"pyproject.toml", detectPyProject},
	{"requirements.txt", detectRequirements},
	{"go.mod", detectGo},
	{"Cargo.toml", detectCargo},
	{"Procfile", detectProcfile},
	{"Makefile", detectMakefile},
	{"index.html", detectStatic},
}

// allowedPrograms are the only executables a recipe command may start with.
// Everything a package manager or interpreter needs is here; shells that
// could fetch and run arbitrary code are not.
var allowedPrograms = map[string]bool{
	"npm": true, "npx": true, "pnpm": true, "yarn": true, "bun": true, "bunx": true, "node": true, "deno": true,
	"pip": true, "pip3": true, "python": true, "python3": true, "poetry": true, "uv": true, "pipenv": true,
	"streamlit": true, "uvicorn": true, "gunicorn": true, "flask": true, "gradio": true,
	"go": true, "cargo": true, "make": true, "bundle": true, "ruby": true, "rails": true,
	"hugo": true, "jekyll": true, "mkdocs": true, "php": true, "composer": true, "dotnet": true,
	"corepack": true, "env": true, "true": true,
}

// forbiddenTokens reject command lines that pipe or substitute in ways the
// allow-list cannot reason about.
var forbiddenTokens = []string{"|", "`", "$(", ">", "<", ";", "&&", "||", "sudo", "curl", "wget", "chmod", "eval"}

// Validate rejects recipes whose commands could escape the allow-list.
func Validate(r Recipe) error {
	if r.Kind != KindWeb && r.Kind != KindTerminal {
		return fmt.Errorf("kind %q must be web or terminal", r.Kind)
	}
	if strings.HasPrefix(r.Cwd, "/") || strings.Contains(r.Cwd, "..") {
		return fmt.Errorf("cwd %q must be relative and inside the repo", r.Cwd)
	}
	if len(r.Install) > 8 {
		return errors.New("too many install commands")
	}
	for _, c := range r.Install {
		if err := validateCommand(c); err != nil {
			return fmt.Errorf("install %q: %w", c, err)
		}
	}
	if r.Kind == KindWeb {
		if r.Start == "" {
			return errors.New("web recipe needs a start command")
		}
		if r.Port < 1 || r.Port > 65535 {
			return fmt.Errorf("port %d out of range", r.Port)
		}
	}
	if r.Start != "" {
		if err := validateCommand(r.Start); err != nil {
			return fmt.Errorf("start %q: %w", r.Start, err)
		}
	}
	for k, v := range r.Env {
		if k == "" || strings.ContainsAny(k, "= \n") || strings.ContainsAny(v, "\n") {
			return fmt.Errorf("env %q is malformed", k)
		}
	}
	return nil
}

// validateCommand checks one shell-free command line: allow-listed program,
// no shell metacharacters, and simple KEY=VALUE prefixes only.
func validateCommand(c string) error {
	c = strings.TrimSpace(c)
	if c == "" {
		return errors.New("empty command")
	}
	for _, t := range forbiddenTokens {
		if strings.Contains(c, t) {
			return fmt.Errorf("contains %q", t)
		}
	}
	fields := strings.Fields(c)
	// Leading KEY=VALUE assignments are environment, not the program.
	i := 0
	for i < len(fields) && strings.Contains(fields[i], "=") && !strings.HasPrefix(fields[i], "-") {
		i++
	}
	if i >= len(fields) {
		return errors.New("no program")
	}
	prog := fields[i]
	if !allowedPrograms[prog] {
		return fmt.Errorf("program %q is not allowed", prog)
	}
	return nil
}

// Split turns a validated command line into program, args, and environment
// assignments so it can run without a shell.
func Split(c string) (env []string, program string, args []string) {
	fields := strings.Fields(c)
	i := 0
	for i < len(fields) && strings.Contains(fields[i], "=") && !strings.HasPrefix(fields[i], "-") {
		env = append(env, fields[i])
		i++
	}
	if i < len(fields) {
		program = fields[i]
		args = fields[i+1:]
	}
	return env, program, args
}

// EnvList renders the recipe env as KEY=VALUE pairs in stable order.
func (r Recipe) EnvList() []string {
	keys := make([]string, 0, len(r.Env))
	for k := range r.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+r.Env[k])
	}
	return out
}
