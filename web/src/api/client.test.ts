import { ApiError, BUSY_RETRY_DELAYS_MS, DEVICE_ID_KEY, createClient, getDeviceId } from './client'

class MemoryStore {
  private m = new Map<string, string>()
  getItem(k: string) {
    return this.m.get(k) ?? null
  }
  setItem(k: string, v: string) {
    this.m.set(k, v)
  }
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

const session = { id: 's1', status: 'pending', build: {}, terminal_ready: false, extended: false }

describe('getDeviceId', () => {
  it('mints a v4 uuid once and reuses it', () => {
    const store = new MemoryStore()
    const a = getDeviceId(store)
    expect(a).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    expect(getDeviceId(store)).toBe(a)
    expect(store.getItem(DEVICE_ID_KEY)).toBe(a)
  })
})

describe('createClient', () => {
  it('sends X-Device-Id and JSON body on createSession', async () => {
    const fetchImpl = vi.fn(async () => jsonResponse(201, session))
    const store = new MemoryStore()
    const client = createClient({ fetchImpl, store })
    const got = await client.createSession('o/r')
    expect(got.id).toBe('s1')
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/v1/sessions')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(JSON.stringify({ repo: 'o/r' }))
    const headers = init.headers as Record<string, string>
    expect(headers['X-Device-Id']).toBe(store.getItem(DEVICE_ID_KEY))
    expect(headers['Content-Type']).toBe('application/json')
  })

  it.each([
    [400, 'invalid'],
    [422, 'unsupported'],
    [404, 'not_found'],
    [429, 'rate_limited'],
    [502, 'infra_error'],
    [500, 'internal'],
  ])('maps %i to ApiError %s with message and retry_after', async (status, code) => {
    const fetchImpl = vi.fn(async () =>
      jsonResponse(status, { error: { code, message: 'nope', retry_after_seconds: 7 } }),
    )
    const client = createClient({ fetchImpl, store: new MemoryStore() })
    const err = await client.getSession('x').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.code).toBe(code)
    expect(err.status).toBe(status)
    expect(err.message).toBe('nope')
    expect(err.retryAfterSeconds).toBe(7)
    expect(fetchImpl).toHaveBeenCalledTimes(1)
  })

  it('falls back to a status-derived code when the body is not JSON', async () => {
    const fetchImpl = vi.fn(async () => new Response('gateway down', { status: 502 }))
    const client = createClient({ fetchImpl, store: new MemoryStore() })
    const err = await client.getBuild('o', 'r').catch((e) => e)
    expect(err.code).toBe('infra_error')
    expect(err.message).toContain('502')
  })

  describe('busy retry', () => {
    beforeEach(() => vi.useFakeTimers())
    afterEach(() => vi.useRealTimers())

    const busy = () => jsonResponse(503, { error: { code: 'busy', message: 'full' } })

    it('retries on the 2s/4s/8s schedule and succeeds', async () => {
      const fetchImpl = vi
        .fn<() => Promise<Response>>()
        .mockResolvedValueOnce(busy())
        .mockResolvedValueOnce(busy())
        .mockResolvedValueOnce(jsonResponse(201, session))
      const onBusyRetry = vi.fn()
      const client = createClient({ fetchImpl, store: new MemoryStore(), onBusyRetry })
      const p = client.createSession('o/r')

      await vi.advanceTimersByTimeAsync(0)
      expect(fetchImpl).toHaveBeenCalledTimes(1)
      expect(onBusyRetry).toHaveBeenLastCalledWith(1, 2000)
      await vi.advanceTimersByTimeAsync(1999)
      expect(fetchImpl).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(1)
      expect(fetchImpl).toHaveBeenCalledTimes(2)
      expect(onBusyRetry).toHaveBeenLastCalledWith(2, 4000)
      await vi.advanceTimersByTimeAsync(4000)
      expect(fetchImpl).toHaveBeenCalledTimes(3)
      await expect(p).resolves.toMatchObject({ id: 's1' })
      expect(onBusyRetry).toHaveBeenCalledTimes(2)
    })

    it('gives up after three retries and throws the busy error', async () => {
      const fetchImpl = vi.fn(async () => busy())
      const client = createClient({ fetchImpl, store: new MemoryStore() })
      const p = client.createSession('o/r')
      const settled = p.catch((e) => e)
      const total = BUSY_RETRY_DELAYS_MS.reduce((a, b) => a + b, 0)
      await vi.advanceTimersByTimeAsync(total)
      const err = await settled
      expect(err).toBeInstanceOf(ApiError)
      expect(err.code).toBe('busy')
      expect(fetchImpl).toHaveBeenCalledTimes(1 + BUSY_RETRY_DELAYS_MS.length)
    })

    it('does not retry non-busy errors', async () => {
      const fetchImpl = vi.fn(async () =>
        jsonResponse(429, { error: { code: 'rate_limited', message: 'slow down' } }),
      )
      const client = createClient({ fetchImpl, store: new MemoryStore() })
      const err = await client.createSession('o/r').catch((e) => e)
      expect(err.code).toBe('rate_limited')
      expect(fetchImpl).toHaveBeenCalledTimes(1)
    })
  })

  it('builds events, pty, and badge URLs', () => {
    const client = createClient({ store: new MemoryStore(), baseUrl: 'https://trylive.dev' })
    expect(client.eventsUrl('a b')).toBe('https://trylive.dev/v1/sessions/a%20b/events')
    expect(client.ptyUrl('s1', 80, 24)).toBe('wss://trylive.dev/v1/sessions/s1/pty?cols=80&rows=24')
    expect(client.badgeUrl('o', 'r')).toBe('https://trylive.dev/badge/gh/o/r.svg')
  })
})
