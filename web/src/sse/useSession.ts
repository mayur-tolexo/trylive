import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react'
import { ApiError, createClient, isApiError } from '../api/client'
import { SSE_EVENT_NAMES, type Session } from '../api/types'
import { initialState, reduce, type SessionEvent } from './reducer'

/** Inputs for useSession: the repo to run and an optional existing session id. */
export interface UseSessionArgs {
  owner: string
  repo: string
  sessionId: string | null
  /** Called once a session exists so the page can pin its id into the URL. */
  onSessionId: (id: string) => void
}

/** Where the hook is in obtaining a session, before the SSE stream takes over. */
export type SetupPhase = 'creating' | 'ready' | 'failed'

/** What the session page consumes. */
export interface UseSessionResult {
  state: ReturnType<typeof reduce>
  setup: SetupPhase
  setupError: ApiError | null
  /** Non-null while POST /v1/sessions is backing off on a `busy` response. */
  busyRetry: { attempt: number; delayMs: number } | null
  extend: () => Promise<void>
  extendError: ApiError | null
  restart: () => void
}

/**
 * useSession owns the session lifecycle: create-or-load, then subscribe to the
 * event stream and fold every event through the pure reducer. `restart` drops
 * the current session and creates a fresh one for the same repo.
 */
export function useSession({ owner, repo, sessionId, onSessionId }: UseSessionArgs): UseSessionResult {
  const [state, dispatch] = useReducer(reduce, initialState)
  const [setup, setSetup] = useState<SetupPhase>('creating')
  const [setupError, setSetupError] = useState<ApiError | null>(null)
  const [extendError, setExtendError] = useState<ApiError | null>(null)
  const [busyRetry, setBusyRetry] = useState<UseSessionResult['busyRetry']>(null)
  // Bumping the generation re-runs setup; a stale id is ignored on restart.
  const [generation, setGeneration] = useState(0)
  const ignoreUrlId = useRef(false)
  const activeId = useRef<string | null>(null)

  const client = useMemo(
    () => createClient({ onBusyRetry: (attempt, delayMs) => setBusyRetry({ attempt, delayMs }) }),
    [],
  )

  // Flow 1: obtain a Session (reuse the URL's id when it still exists, else create).
  useEffect(() => {
    // The URL catching up to the id we already hold is not a new session.
    if (sessionId && activeId.current === sessionId) return
    let cancelled = false
    setSetup('creating')
    setSetupError(null)
    setBusyRetry(null)
    activeId.current = null

    async function run(): Promise<Session> {
      const wanted = ignoreUrlId.current ? null : sessionId
      if (wanted) {
        try {
          return await client.getSession(wanted)
        } catch (err) {
          // A stale share link: fall through and start a fresh session.
          if (!(isApiError(err) && err.code === 'not_found')) throw err
        }
      }
      return client.createSession(`${owner}/${repo}`)
    }

    run()
      .then((s) => {
        if (cancelled) return
        ignoreUrlId.current = false
        activeId.current = s.id
        dispatch({ type: 'session', data: s })
        setBusyRetry(null)
        setSetup('ready')
        onSessionId(s.id)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setBusyRetry(null)
        setSetupError(isApiError(err) ? err : new ApiError('internal', 0, String(err)))
        setSetup('failed')
      })
    return () => {
      cancelled = true
    }
    // onSessionId is intentionally excluded: it is a navigation callback whose
    // identity changes per render and must not restart the session.
  }, [client, owner, repo, sessionId, generation])

  // Flow 2: subscribe to the event stream once a session exists, until it ends.
  const id = setup === 'ready' ? activeId.current : null
  useEffect(() => {
    if (!id) return
    const es = new EventSource(client.eventsUrl(id))
    const onOpen = () => dispatch({ type: 'sse_open' })
    es.addEventListener('open', onOpen)
    for (const name of SSE_EVENT_NAMES) {
      es.addEventListener(name, (ev: Event) => {
        // A transport failure also arrives as an 'error' Event; only MessageEvents carry data.
        if (!(ev instanceof MessageEvent)) {
          if (name === 'error') dispatch({ type: 'sse_error' })
          return
        }
        let data: unknown
        try {
          data = JSON.parse(ev.data as string)
        } catch {
          return
        }
        dispatch({ type: name, data } as SessionEvent)
        // Nothing follows `ended`; stop EventSource from reconnecting forever.
        if (name === 'ended') es.close()
      })
    }
    return () => es.close()
  }, [client, id])

  const extend = useCallback(async () => {
    if (!activeId.current) return
    setExtendError(null)
    try {
      const r = await client.extendSession(activeId.current)
      dispatch({ type: 'extended', data: { expires_at: r.expires_at } })
    } catch (err) {
      setExtendError(isApiError(err) ? err : new ApiError('internal', 0, String(err)))
    }
  }, [client])

  const restart = useCallback(() => {
    ignoreUrlId.current = true
    activeId.current = null
    dispatch({ type: 'reset' })
    setGeneration((g) => g + 1)
  }, [])

  return { state, setup, setupError, busyRetry, extend, extendError, restart }
}

