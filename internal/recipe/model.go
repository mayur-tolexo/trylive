package recipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// LLM answers one prompt with text; the production implementation is an
// OpenAI-compatible chat endpoint.
type LLM interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Infer asks the model for a recipe when no detector matched. The reply must
// be a JSON object matching Recipe and pass Validate, or the repo is
// unsupported.
func Infer(ctx context.Context, llm LLM, m Manifest) (Recipe, error) {
	if llm == nil {
		return Recipe{}, ErrNoMatch
	}
	reply, err := llm.Complete(ctx, inferSystem, manifestPrompt(m))
	if err != nil {
		return Recipe{}, err
	}
	r, err := parseRecipeJSON(reply)
	if err != nil {
		return Recipe{}, err
	}
	r.Detector = "model"
	if err := Validate(r); err != nil {
		return Recipe{}, fmt.Errorf("model recipe rejected: %w", err)
	}
	return r, nil
}

// Repair asks the model for revised install commands after a failure. It
// returns the new install list, validated.
func Repair(ctx context.Context, llm LLM, m Manifest, r Recipe, failed string, logTail string) ([]string, error) {
	if llm == nil {
		return nil, errors.New("no model configured")
	}
	user := manifestPrompt(m) + fmt.Sprintf("\n\nCurrent recipe:\n%s\n\nThe command %q failed. Last output:\n%s\n\nReturn ONLY a JSON array of replacement install commands (strings), nothing else.",
		mustJSON(r), failed, tail(logTail, 3000))
	reply, err := llm.Complete(ctx, repairSystem, user)
	if err != nil {
		return nil, err
	}
	var cmds []string
	if err := json.Unmarshal([]byte(extractJSON(reply)), &cmds); err != nil {
		return nil, fmt.Errorf("repair reply is not a JSON array: %w", err)
	}
	if len(cmds) == 0 || len(cmds) > 8 {
		return nil, errors.New("repair produced no usable commands")
	}
	for _, c := range cmds {
		if err := validateCommand(c); err != nil {
			return nil, fmt.Errorf("repair command %q: %w", c, err)
		}
	}
	return cmds, nil
}

const inferSystem = `You decide how to install and run a GitHub repository inside a Linux sandbox that has Node 20, Python 3, Go, Rust and make installed, with the repository cloned into the current directory. Reply with ONE JSON object and nothing else:
{"kind":"web"|"terminal","cwd":"","install":["..."],"start":"...","port":3000,"env":{"KEY":"VALUE"}}
Rules: commands are single programs with arguments, no shell operators (no |, &&, ;, >, $(...)), no sudo, curl or wget. Allowed programs: npm npx pnpm yarn bun node deno pip pip3 python3 poetry uv go cargo make bundle ruby streamlit uvicorn gunicorn flask hugo jekyll mkdocs php composer dotnet. Servers must bind 0.0.0.0 (pass --host 0.0.0.0 or set HOST/PORT env). kind is "web" only if start launches a server on port; otherwise "terminal" with start omitted. Prefer the repository's own documented commands from the README.`

const repairSystem = `You fix a failing install step for a repository inside a Linux sandbox. Same command rules: single programs with arguments, no shell operators, no sudo/curl/wget, allowed programs only. Reply with ONLY a JSON array of strings.`

// manifestPrompt renders the manifest compactly for the model.
func manifestPrompt(m Manifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Top-level files: %s\n", strings.Join(m.Files, ", "))
	if m.PackageJSON != nil {
		fmt.Fprintf(&b, "package.json: %s\n", mustJSON(m.PackageJSON))
	}
	for name, body := range map[string]string{
		"pyproject.toml": m.PyProject, "requirements.txt": m.Requirements, "go.mod": m.GoMod,
		"Cargo.toml": m.CargoToml, "Procfile": m.Procfile, "Dockerfile": m.Dockerfile,
	} {
		if strings.TrimSpace(body) != "" {
			fmt.Fprintf(&b, "\n--- %s ---\n%s\n", name, tail(body, 2000))
		}
	}
	if len(m.MakeTargets) > 0 {
		fmt.Fprintf(&b, "Makefile targets: %s\n", strings.Join(m.MakeTargets, ", "))
	}
	if strings.TrimSpace(m.Readme) != "" {
		fmt.Fprintf(&b, "\n--- README ---\n%s\n", tail(m.Readme, 6000))
	}
	return b.String()
}

var jsonBlockRE = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// extractJSON strips a code fence and any prose around the first JSON value.
func extractJSON(s string) string {
	if m := jsonBlockRE.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	s = strings.TrimSpace(s)
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return s
	}
	end := strings.LastIndexAny(s, "}]")
	if end < start {
		return s
	}
	return s[start : end+1]
}

// parseRecipeJSON decodes the model's object, tolerating a fenced reply.
func parseRecipeJSON(reply string) (Recipe, error) {
	var r Recipe
	if err := json.Unmarshal([]byte(extractJSON(reply)), &r); err != nil {
		return Recipe{}, fmt.Errorf("model reply is not a recipe: %w", err)
	}
	if r.Install == nil {
		r.Install = []string{}
	}
	return r, nil
}

// tail returns the last n bytes of s.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// mustJSON marshals for prompts; a failure here is a programming error.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// OpenAICompat is the production LLM: any OpenAI-compatible chat endpoint.
// Thinking is disabled because reasoning models otherwise spend the whole
// budget on hidden reasoning and return empty content.
type OpenAICompat struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

// NewOpenAICompat returns a client with a timeout suited to one-shot answers.
func NewOpenAICompat(baseURL, apiKey, model string) *OpenAICompat {
	return &OpenAICompat{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

func (c *OpenAICompat) Complete(ctx context.Context, system, user string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":                c.Model,
		"messages":             []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"max_tokens":           900,
		"temperature":          0.1,
		"thinking":             map[string]string{"type": "disabled"},
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || resp.StatusCode >= 300 || out.Error != nil {
		return "", fmt.Errorf("llm: status %d: %s", resp.StatusCode, tail(string(raw), 300))
	}
	if len(out.Choices) == 0 {
		return "", errors.New("llm: no choices")
	}
	return out.Choices[0].Message.Content, nil
}
