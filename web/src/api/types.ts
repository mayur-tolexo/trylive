/** Session lifecycle as reported by the backend. */
export type SessionStatus = 'pending' | 'building' | 'starting' | 'live' | 'ended'

/** Build lifecycle; `unsupported` means no recipe could be detected. */
export type BuildStatus = 'queued' | 'building' | 'ready' | 'failed' | 'unsupported'

/** How the repo is exposed once running: a served port or a bare shell. */
export type BuildKind = 'web' | 'terminal'

/** The detected (or trylive.yaml-declared) way to run the repo. */
export interface Recipe {
  kind: BuildKind
  cwd: string
  install: string[]
  start: string
  port: number
  env: Record<string, string>
  detector: string
}

/** One immutable build of a repo at a specific commit. */
export interface Build {
  id: string
  owner: string
  repo: string
  sha: string
  status: BuildStatus
  kind?: BuildKind
  recipe?: Recipe
  error?: string
  built_at?: string
  visits: number
}

/** A visitor's live sandbox restored from a Build. */
export interface Session {
  id: string
  status: SessionStatus
  build: Build
  preview_url?: string
  terminal_ready: boolean
  expires_at?: string
  extended: boolean
  ended_reason?: string
}

/** Response of POST /v1/sessions/{id}/extend. */
export interface ExtendResponse {
  expires_at: string
  extended: true
}

/** Error codes the API may return in its `error.code` envelope. */
export type ApiErrorCode =
  | 'invalid'
  | 'unsupported'
  | 'not_found'
  | 'rate_limited'
  | 'busy'
  | 'infra_error'
  | 'internal'

/** Wire shape of an API error body. */
export interface ApiErrorBody {
  error: {
    code: ApiErrorCode
    message: string
    retry_after_seconds?: number
  }
}

/** Payloads of the SSE events on /v1/sessions/{id}/events, keyed by event name. */
export interface SseEvents {
  log: { ts: string; phase: string; line: string }
  phase: { build_status: string; session_status: string; message: string }
  preview: { url: string; port: number }
  terminal: { ready: true }
  ttl: { expires_at: string; extended: boolean }
  ended: { reason: string }
  error: { code: string; message: string }
}

/** Names of SSE events the client subscribes to. */
export const SSE_EVENT_NAMES = ['log', 'phase', 'preview', 'terminal', 'ttl', 'ended', 'error'] as const
