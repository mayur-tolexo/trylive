import type { ApiErrorBody, ApiErrorCode, Build, ExtendResponse, Session } from './types'

/** localStorage key holding the anonymous per-browser device id. */
export const DEVICE_ID_KEY = 'trylive.device_id'

/** Backoff schedule (ms) for `busy` responses: 3 retries at 2s, 4s, 8s. */
export const BUSY_RETRY_DELAYS_MS = [2000, 4000, 8000]

/** ApiError is thrown for any non-2xx response, carrying the backend's code. */
export class ApiError extends Error {
  readonly code: ApiErrorCode
  readonly status: number
  readonly retryAfterSeconds?: number

  constructor(code: ApiErrorCode, status: number, message: string, retryAfterSeconds?: number) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.status = status
    this.retryAfterSeconds = retryAfterSeconds
  }
}

/** isApiError narrows an unknown thrown value to ApiError. */
export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError
}

/** Minimal key/value store; localStorage in the browser, a Map in tests. */
export interface KeyValueStore {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

/** randomUuid returns a v4 UUID, falling back to Math.random when crypto is absent. */
export function randomUuid(): string {
  const c = globalThis.crypto
  if (c && typeof c.randomUUID === 'function') return c.randomUUID()
  // RFC 4122 v4 layout: 8-4-4-4-12 hex with version and variant nibbles set.
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (ch) => {
    const r = (Math.random() * 16) | 0
    const v = ch === 'x' ? r : (r & 0x3) | 0x8
    return v.toString(16)
  })
}

/** getDeviceId returns the stored device id, minting and persisting one on first use. */
export function getDeviceId(store: KeyValueStore): string {
  try {
    const existing = store.getItem(DEVICE_ID_KEY)
    if (existing) return existing
    const id = randomUuid()
    store.setItem(DEVICE_ID_KEY, id)
    return id
  } catch {
    // Storage may be blocked (private mode); fall back to a per-load id.
    return randomUuid()
  }
}

/** parseErrorBody maps a failed Response to an ApiError, tolerating non-JSON bodies. */
export async function parseErrorBody(res: Response): Promise<ApiError> {
  let body: Partial<ApiErrorBody> | undefined
  try {
    body = (await res.json()) as ApiErrorBody
  } catch {
    body = undefined
  }
  const err = body?.error
  const code: ApiErrorCode = err?.code ?? statusToCode(res.status)
  const message = err?.message || `${res.status} ${res.statusText || 'request failed'}`
  return new ApiError(code, res.status, message, err?.retry_after_seconds)
}

/** statusToCode gives a best-effort code when the body carries none. */
function statusToCode(status: number): ApiErrorCode {
  switch (status) {
    case 400:
      return 'invalid'
    case 404:
      return 'not_found'
    case 422:
      return 'unsupported'
    case 429:
      return 'rate_limited'
    case 502:
      return 'infra_error'
    case 503:
      return 'busy'
    default:
      return 'internal'
  }
}

/** Dependencies the client needs; all injectable so tests stay pure. */
export interface ClientOptions {
  fetchImpl?: typeof fetch
  store?: KeyValueStore
  baseUrl?: string
  /** Called before each busy retry with the 1-based attempt and the delay. */
  onBusyRetry?: (attempt: number, delayMs: number) => void
  retryDelaysMs?: number[]
}

/** The typed API surface used by the pages. */
export interface ApiClient {
  createSession(repo: string): Promise<Session>
  getSession(id: string): Promise<Session>
  extendSession(id: string): Promise<ExtendResponse>
  getBuild(owner: string, repo: string): Promise<Build>
  eventsUrl(id: string): string
  ptyUrl(id: string, cols: number, rows: number): string
  badgeUrl(owner: string, repo: string): string
  deviceId(): string
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

/**
 * createClient builds an ApiClient. Every /v1 call sends X-Device-Id; `busy`
 * (503) responses are retried on the backoff schedule before surfacing.
 */
export function createClient(opts: ClientOptions = {}): ApiClient {
  const fetchImpl = opts.fetchImpl ?? ((input, init) => fetch(input, init))
  const store = opts.store ?? globalThis.localStorage
  const baseUrl = opts.baseUrl ?? ''
  const delays = opts.retryDelaysMs ?? BUSY_RETRY_DELAYS_MS

  const deviceId = () => getDeviceId(store)

  async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
    // Flow: one initial attempt plus one retry per backoff slot; only `busy`
    // triggers a retry, every other failure is thrown immediately.
    for (let attempt = 0; ; attempt++) {
      const res = await fetchImpl(baseUrl + path, {
        method,
        headers: {
          Accept: 'application/json',
          'X-Device-Id': deviceId(),
          ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        },
        body: body !== undefined ? JSON.stringify(body) : undefined,
      })
      if (res.ok) return (await res.json()) as T
      const err = await parseErrorBody(res)
      if (err.code !== 'busy' || attempt >= delays.length) throw err
      const delay = delays[attempt]
      opts.onBusyRetry?.(attempt + 1, delay)
      await sleep(delay)
    }
  }

  return {
    createSession: (repo) => request<Session>('POST', '/v1/sessions', { repo }),
    getSession: (id) => request<Session>('GET', `/v1/sessions/${encodeURIComponent(id)}`),
    extendSession: (id) =>
      request<ExtendResponse>('POST', `/v1/sessions/${encodeURIComponent(id)}/extend`),
    getBuild: (owner, repo) =>
      request<Build>('GET', `/v1/builds/gh/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`),
    eventsUrl: (id) => `${baseUrl}/v1/sessions/${encodeURIComponent(id)}/events`,
    ptyUrl: (id, cols, rows) => {
      // EventSource and fetch accept relative URLs; WebSocket needs an absolute ws(s) URL.
      const origin = baseUrl || (typeof window !== 'undefined' ? window.location.origin : '')
      const wsOrigin = origin.replace(/^http/, 'ws')
      return `${wsOrigin}/v1/sessions/${encodeURIComponent(id)}/pty?cols=${cols}&rows=${rows}`
    },
    badgeUrl: (owner, repo) =>
      `${baseUrl}/badge/gh/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}.svg`,
    deviceId,
  }
}

/** Default client bound to the current origin and localStorage. */
export const api: ApiClient = createClient()
