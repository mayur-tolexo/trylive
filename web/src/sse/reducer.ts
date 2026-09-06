import type { BuildStatus, Session, SessionStatus, SseEvents } from '../api/types'

/** Maximum build-log lines kept in memory; older lines are dropped. */
export const MAX_LOG_LINES = 2000

/** One build-log line as received on the `log` event. */
export type LogLine = SseEvents['log']

/** Everything the session page renders, folded from the REST snapshot and SSE stream. */
export interface SessionState {
  session: Session | null
  sessionStatus: SessionStatus
  buildStatus: BuildStatus
  phaseMessage: string
  logs: LogLine[]
  previewUrl?: string
  previewPort?: number
  terminalReady: boolean
  expiresAt?: string
  extended: boolean
  endedReason?: string
  error?: { code: string; message: string }
  /** SSE transport state; `reconnecting` after EventSource reports an error. */
  connection: 'idle' | 'open' | 'reconnecting'
}

/** SSE events tagged by name, plus local events from REST calls and the transport. */
export type SessionEvent =
  | { [K in keyof SseEvents]: { type: K; data: SseEvents[K] } }[keyof SseEvents]
  | { type: 'session'; data: Session }
  | { type: 'extended'; data: { expires_at: string } }
  | { type: 'sse_open' }
  | { type: 'sse_error' }
  | { type: 'reset' }

/** initialState is the empty pre-session state. */
export const initialState: SessionState = {
  session: null,
  sessionStatus: 'pending',
  buildStatus: 'queued',
  phaseMessage: '',
  logs: [],
  terminalReady: false,
  extended: false,
  connection: 'idle',
}

const SESSION_STATUSES: readonly SessionStatus[] = ['pending', 'building', 'starting', 'live', 'ended']
const BUILD_STATUSES: readonly BuildStatus[] = ['queued', 'building', 'ready', 'failed', 'unsupported']

/** asSessionStatus keeps unknown wire strings from poisoning the state. */
function asSessionStatus(s: string, fallback: SessionStatus): SessionStatus {
  return (SESSION_STATUSES as readonly string[]).includes(s) ? (s as SessionStatus) : fallback
}

/** asBuildStatus keeps unknown wire strings from poisoning the state. */
function asBuildStatus(s: string, fallback: BuildStatus): BuildStatus {
  return (BUILD_STATUSES as readonly string[]).includes(s) ? (s as BuildStatus) : fallback
}

/**
 * reduce folds one event into the state. Pure: never mutates its input.
 * Terminal states are sticky: nothing after `ended` can revive the session.
 */
export function reduce(state: SessionState, event: SessionEvent): SessionState {
  switch (event.type) {
    case 'session': {
      const s = event.data
      return {
        ...state,
        session: s,
        sessionStatus: s.status,
        buildStatus: s.build?.status ?? state.buildStatus,
        previewUrl: s.preview_url ?? state.previewUrl,
        terminalReady: s.terminal_ready || state.terminalReady,
        expiresAt: s.expires_at ?? state.expiresAt,
        extended: s.extended || state.extended,
        endedReason: s.ended_reason ?? state.endedReason,
      }
    }
    case 'log': {
      const logs = state.logs.length >= MAX_LOG_LINES
        ? [...state.logs.slice(state.logs.length - MAX_LOG_LINES + 1), event.data]
        : [...state.logs, event.data]
      return { ...state, logs }
    }
    case 'phase': {
      if (state.sessionStatus === 'ended') return { ...state, phaseMessage: event.data.message }
      return {
        ...state,
        phaseMessage: event.data.message ?? '',
        buildStatus: asBuildStatus(event.data.build_status, state.buildStatus),
        sessionStatus: asSessionStatus(event.data.session_status, state.sessionStatus),
      }
    }
    case 'preview': {
      if (state.sessionStatus === 'ended') return state
      // A preview URL means the build is ready and the sandbox is serving.
      return {
        ...state,
        previewUrl: event.data.url,
        previewPort: event.data.port,
        buildStatus: 'ready',
        sessionStatus: 'live',
      }
    }
    case 'terminal':
      return { ...state, terminalReady: Boolean(event.data.ready) }
    case 'ttl':
      return { ...state, expiresAt: event.data.expires_at, extended: event.data.extended }
    case 'extended':
      return { ...state, expiresAt: event.data.expires_at, extended: true }
    case 'ended':
      return { ...state, sessionStatus: 'ended', endedReason: event.data.reason, terminalReady: false }
    case 'error':
      return { ...state, error: { code: event.data.code, message: event.data.message } }
    case 'sse_open':
      // The server replays log history on every connect; drop ours to avoid duplicates.
      return { ...state, connection: 'open', logs: [] }
    case 'sse_error':
      return { ...state, connection: 'reconnecting' }
    case 'reset':
      return initialState
    default:
      return state
  }
}
