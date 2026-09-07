// Command trylive serves the API and web app. Configuration is by environment;
// see .env.example.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mayur-tolexo/trylive/internal/builder"
	"github.com/mayur-tolexo/trylive/internal/httpapi"
	"github.com/mayur-tolexo/trylive/internal/recipe"
	"github.com/mayur-tolexo/trylive/internal/repo"
	"github.com/mayur-tolexo/trylive/internal/sandbox"
	"github.com/mayur-tolexo/trylive/internal/session"
	"github.com/mayur-tolexo/trylive/internal/store"
	"github.com/mayur-tolexo/trylive/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// run wires every dependency from the environment and serves until a signal.
func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sb, err := sandbox.NewHTTP(sandbox.Config{
		APIBase: envOr("NEEV_API_BASE", "https://api.ai.neevcloud.com/agent"), APIKey: os.Getenv("NEEV_API_KEY"),
		OrgID: os.Getenv("NEEV_ORG_ID"), ProjectID: os.Getenv("NEEV_PROJECT_ID"), Region: os.Getenv("NEEV_REGION"),
	})
	if err != nil {
		return fmt.Errorf("sandbox platform: %w (set NEEV_API_KEY, NEEV_ORG_ID, NEEV_PROJECT_ID)", err)
	}

	var st store.Store
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pg, err := store.OpenPostgres(ctx, dsn)
		if err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		defer pg.Close()
		st = pg
	} else {
		log.Warn("DATABASE_URL not set; using in-memory store (builds and sessions are lost on restart)")
		st = store.NewMemory()
	}

	var llm recipe.LLM
	if base := os.Getenv("LLM_BASE_URL"); base != "" {
		llm = recipe.NewOpenAICompat(base, os.Getenv("LLM_API_KEY"), envOr("LLM_MODEL", "glm-5-2"))
	} else {
		log.Warn("LLM_BASE_URL not set; repos without a recognised layout will be unsupported")
	}
	if os.Getenv("GITHUB_TOKEN") == "" {
		log.Warn("GITHUB_TOKEN not set; GitHub allows only 60 lookups per hour without one")
	}

	bld := &builder.Builder{Sandbox: sb, Store: st, LLM: llm, Log: log, Concurrency: envInt("BUILD_CONCURRENCY", 5)}
	limits := session.DefaultLimits
	limits.Global = envInt("MAX_LIVE_SESSIONS", limits.Global)
	limits.PerIP = envInt("MAX_SESSIONS_PER_IP", limits.PerIP)
	mgr := &session.Manager{Store: st, Sandbox: sb, Builder: bld, Resolver: repo.NewGitHub(os.Getenv("GITHUB_TOKEN")), Limits: limits, Log: log}
	srv := &httpapi.Server{Sessions: mgr, Store: st, Static: web.Dist(), Log: log}
	if srv.Static == nil {
		log.Warn("web/dist not built; serving API only")
	}

	// When running inside a platform sandbox, keep that sandbox from being
	// idle-paused: preview traffic is not data-plane activity in the platform's
	// eyes, so the server vouches for itself.
	if self := os.Getenv("SELF_SANDBOX_ID"); self != "" {
		go func() {
			t := time.NewTicker(5 * time.Minute)
			defer t.Stop()
			for {
				if err := sb.Keepalive(ctx, self); err != nil {
					log.Warn("self keepalive", "err", err)
				}
				select {
				case <-t.C:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	addr := envOr("ADDR", ":8080")
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		hs.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "addr", addr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// envOr returns the variable or a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt parses an integer variable, falling back to def when unset or invalid.
func envInt(key string, def int) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return def
	}
	return n
}
