import type { Session } from '../api/types'
import { MAX_LOG_LINES, initialState, reduce, type SessionState } from './reducer'

const log = (i: number) => ({ type: 'log' as const, data: { ts: 't', phase: 'install', line: `l${i}` } })

const session: Session = {
  id: 's1',
  status: 'building',
  build: { id: 'b1', owner: 'o', repo: 'r', sha: 'abc1234', status: 'building', visits: 3 },
  terminal_ready: false,
  extended: false,
  expires_at: '2026-01-01T00:00:00Z',
}

describe('reduce', () => {
  it('loads the REST snapshot', () => {
    const s = reduce(initialState, { type: 'session', data: session })
    expect(s.session).toBe(session)
    expect(s.sessionStatus).toBe('building')
    expect(s.buildStatus).toBe('building')
    expect(s.expiresAt).toBe(session.expires_at)
  })

  it('appends log lines without mutating the previous state', () => {
    const s1 = reduce(initialState, log(1))
    const s2 = reduce(s1, log(2))
    expect(s1.logs).toHaveLength(1)
    expect(s2.logs.map((l) => l.line)).toEqual(['l1', 'l2'])
  })

  it(`caps logs at ${MAX_LOG_LINES} lines, dropping the oldest`, () => {
    let s: SessionState = initialState
    for (let i = 0; i < MAX_LOG_LINES + 5; i++) s = reduce(s, log(i))
    expect(s.logs).toHaveLength(MAX_LOG_LINES)
    expect(s.logs[0].line).toBe('l5')
    expect(s.logs[s.logs.length - 1].line).toBe(`l${MAX_LOG_LINES + 4}`)
  })

  it('applies phase updates and ignores unknown statuses', () => {
    const s = reduce(initialState, {
      type: 'phase',
      data: { build_status: 'building', session_status: 'starting', message: 'installing deps' },
    })
    expect(s.buildStatus).toBe('building')
    expect(s.sessionStatus).toBe('starting')
    expect(s.phaseMessage).toBe('installing deps')
    const s2 = reduce(s, { type: 'phase', data: { build_status: 'bogus', session_status: '???', message: '' } })
    expect(s2.buildStatus).toBe('building')
    expect(s2.sessionStatus).toBe('starting')
  })

  it('preview sets the url, marks the build ready and the session live', () => {
    const s = reduce(initialState, { type: 'preview', data: { url: 'https://p.example/x', port: 3000 } })
    expect(s.previewUrl).toBe('https://p.example/x')
    expect(s.previewPort).toBe(3000)
    expect(s.buildStatus).toBe('ready')
    expect(s.sessionStatus).toBe('live')
  })

  it('terminal ready flips the flag', () => {
    expect(reduce(initialState, { type: 'terminal', data: { ready: true } }).terminalReady).toBe(true)
  })

  it('ttl and extend update the expiry', () => {
    const s = reduce(initialState, { type: 'ttl', data: { expires_at: 'A', extended: false } })
    expect(s.expiresAt).toBe('A')
    expect(s.extended).toBe(false)
    const s2 = reduce(s, { type: 'extended', data: { expires_at: 'B' } })
    expect(s2.expiresAt).toBe('B')
    expect(s2.extended).toBe(true)
  })

  it('ended is sticky against later phase and preview events', () => {
    const s = reduce(initialState, { type: 'ended', data: { reason: 'ttl' } })
    expect(s.sessionStatus).toBe('ended')
    expect(s.endedReason).toBe('ttl')
    const s2 = reduce(s, { type: 'preview', data: { url: 'u', port: 1 } })
    expect(s2.sessionStatus).toBe('ended')
    expect(s2.previewUrl).toBeUndefined()
    const s3 = reduce(s2, { type: 'phase', data: { build_status: 'ready', session_status: 'live', message: 'm' } })
    expect(s3.sessionStatus).toBe('ended')
  })

  it('records errors', () => {
    const s = reduce(initialState, { type: 'error', data: { code: 'infra_error', message: 'boom' } })
    expect(s.error).toEqual({ code: 'infra_error', message: 'boom' })
  })

  it('reset returns to the initial state', () => {
    const s = reduce(reduce(initialState, log(1)), { type: 'ended', data: { reason: 'ttl' } })
    expect(reduce(s, { type: 'reset' })).toBe(initialState)
  })

  it('clears replayed logs on (re)connect and flags reconnecting on error', () => {
    const s = reduce(reduce(initialState, log(1)), { type: 'sse_open' })
    expect(s.logs).toEqual([])
    expect(s.connection).toBe('open')
    expect(reduce(s, { type: 'sse_error' }).connection).toBe('reconnecting')
  })
})
