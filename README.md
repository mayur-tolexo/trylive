# trylive

One click runs any public GitHub repository in your browser. No install, no
login. Put the badge in your README and every reader can try the project
live in a few seconds:

```markdown
[![Try it live](https://trylive.dev/badge/gh/OWNER/REPO.svg)](https://trylive.dev/gh/OWNER/REPO)
```

## How it works

The first visit to a commit is a **build**: a sandbox clones the repo, an
inspector reports what it finds (`package.json`, `pyproject.toml`, `go.mod`,
`Procfile`, `Makefile`, …), deterministic detectors turn that into a
*recipe* (install commands, start command, port), the recipe runs, the
sandbox watches for a listening port, and then — with the server still
running — the whole sandbox is **snapshotted**. The visitor watches all of
this happen in a streamed log.

Every later visit **restores** that snapshot into a fresh sandbox: the app is
already installed and already listening, so a click becomes a live URL in a
few seconds. The build sandbox stays paused as the snapshot's owner.

If no detector recognises the repo, a model reads the README and manifests
and proposes a recipe; if an install step fails, the model gets two chances
to fix it. Every command a recipe runs must start with an allow-listed
program and contain no shell operators. If nothing ends up listening, the
visitor gets a terminal in the installed repo instead of a preview.

Maintainers can skip detection entirely with a `trylive.yaml` at the repo
root:

```yaml
install:
  - npm install
start: npm run dev -- --host 0.0.0.0
port: 5173
env:
  SOME_FLAG: "1"
```

## Sessions

Visitors are anonymous. A session lasts 15 minutes of activity and can be
extended once to 30; the platform reclaims the sandbox afterwards. Visitor
sandboxes have no outbound network. One live session per device, a few per
IP, and a global cap keep the free tier honest.

## Running it

Requirements: Go 1.26+, Node 22+, and NeevCloud sandbox credentials.

```sh
cp .env.example .env   # fill in NEEV_API_KEY, NEEV_ORG_ID, NEEV_PROJECT_ID
cd web && npm ci && npm run build && cd ..
make run
# open http://localhost:8080
```

`deploy/docker-compose.yml` starts Postgres plus the server;
`deploy/Dockerfile` builds the production image.

| Variable | Purpose |
|---|---|
| `NEEV_API_KEY`, `NEEV_ORG_ID`, `NEEV_PROJECT_ID`, `NEEV_API_BASE`, `NEEV_REGION` | sandbox platform (required) |
| `DATABASE_URL` | Postgres; unset uses an in-memory store |
| `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL` | model for detection fallback and install repair |
| `GITHUB_TOKEN` | raises GitHub lookups from 60 to 5000 per hour |
| `BUILD_CONCURRENCY`, `MAX_LIVE_SESSIONS`, `MAX_SESSIONS_PER_IP` | capacity |

## API

| Route | Purpose |
|---|---|
| `POST /v1/sessions {repo}` | start a session; joins or starts the build |
| `GET /v1/sessions/{id}` | current state |
| `GET /v1/sessions/{id}/events` | server-sent events: `log`, `phase`, `preview`, `terminal`, `ttl`, `ended`, `error` |
| `GET /v1/sessions/{id}/pty` | websocket terminal (binary I/O, JSON control frames) |
| `POST /v1/sessions/{id}/extend` | the one extension |
| `GET /v1/builds/gh/{owner}/{repo}` | latest build and its recipe |
| `GET /badge/gh/{owner}/{repo}.svg` | the badge |

## Development

```sh
make lint   # gofmt + vet
make test   # Go (+ Postgres conformance with TRYLIVE_TEST_DATABASE_URL) and web tests
```

Live tests against the platform run only when the `NEEV_*` variables are set.

## License

MIT
