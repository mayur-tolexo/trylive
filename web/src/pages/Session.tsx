import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { BuildKind } from '../api/types'
import { BuildLog } from '../components/BuildLog'
import { Funnel } from '../components/Funnel'
import { Preview } from '../components/Preview'
import { Recipe } from '../components/Recipe'
import { Toast, type ToastMessage } from '../components/Toast'
import { TopBar } from '../components/TopBar'
import { Unsupported } from '../components/Unsupported'
import { badgeMarkdown } from '../lib/badge'
import { copyText } from '../lib/clipboard'
import { useSession } from '../sse/useSession'
import { Terminal } from '../terminal/Terminal'

/**
 * Session page: creates or resumes a session for /gh/:owner/:repo, streams the
 * build, then swaps the log for the preview (web) or the shell (terminal).
 */
export function Session() {
  const { owner = '', repo = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const sessionId = params.get('s')

  const onSessionId = useCallback(
    (id: string) => {
      if (params.get('s') !== id) setParams({ s: id }, { replace: true })
    },
    [params, setParams],
  )

  const { state, setup, setupError, busyRetry, extend, extendError, restart } = useSession({
    owner,
    repo,
    sessionId,
    onSessionId,
  })

  const [toasts, setToasts] = useState<ToastMessage[]>([])
  const dismiss = useCallback((id: number) => setToasts((t) => t.filter((x) => x.id !== id)), [])
  const toast = useCallback(
    (text: string, tone: ToastMessage['tone'] = 'info') =>
      setToasts((t) => [...t, { id: Date.now() + Math.random(), text, tone }]),
    [],
  )

  useEffect(() => {
    if (extendError) toast(extendError.message || 'Could not extend the session.', 'danger')
  }, [extendError, toast])

  useEffect(() => {
    if (setupError?.code === 'rate_limited') {
      toast(`Too many sessions. Try again in ${setupError.retryAfterSeconds ?? 60}s.`, 'danger')
    }
  }, [setupError, toast])

  useEffect(() => {
    document.title = `${owner}/${repo} · trylive`
    return () => {
      document.title = 'trylive'
    }
  }, [owner, repo])

  const build = state.session?.build
  const recipe = build?.recipe
  const kind: BuildKind = recipe?.kind ?? build?.kind ?? (state.previewUrl ? 'web' : 'terminal')
  const ended = state.sessionStatus === 'ended'
  // Once live, the preview stays mounted under the ended overlay instead of
  // falling back to the build log.
  const reachedLive = Boolean(state.previewUrl) || state.sessionStatus === 'live'
  const live = !ended && reachedLive
  const buildFailed = state.buildStatus === 'failed'
  const buildUnsupported = state.buildStatus === 'unsupported' || setupError?.code === 'unsupported'

  // The drawer opens by itself for terminal-kind repos once the shell is ready.
  const [drawerOpen, setDrawerOpen] = useState(false)
  useEffect(() => {
    if (live && kind === 'terminal') setDrawerOpen(true)
  }, [live, kind])

  const copyBadge = async () => {
    const ok = await copyText(badgeMarkdown(window.location.origin, owner, repo))
    toast(ok ? 'Badge markdown copied' : 'Copy failed', ok ? 'info' : 'danger')
  }
  const share = async () => {
    const ok = await copyText(`${window.location.origin}/gh/${owner}/${repo}`)
    toast(ok ? 'Link copied' : 'Copy failed', ok ? 'info' : 'danger')
  }

  const sid = state.session?.id
  const ptyUrl = useMemo(() => (cols: number, rows: number) => api.ptyUrl(sid ?? '', cols, rows), [sid])
  const connectTerminal = Boolean(sid) && state.terminalReady && !ended

  return (
    <div className="session">
      <TopBar
        owner={owner}
        repo={repo}
        sha={build?.sha}
        sessionStatus={state.sessionStatus}
        buildStatus={state.buildStatus}
        expiresAt={state.expiresAt}
        extended={state.extended}
        onExtend={() => void extend()}
        onCopyBadge={() => void copyBadge()}
        onShare={() => void share()}
      />

      <div className="session-body">
        <main className="session-main">
          {busyRetry && (
            <div className="banner" aria-live="polite">
              Queue is full, retrying in {busyRetry.delayMs / 1000}s ({busyRetry.attempt}/3)…
            </div>
          )}
          {state.connection === 'reconnecting' && !ended && (
            <div className="banner muted" aria-live="polite">
              Reconnecting to the build stream…
            </div>
          )}

          {setup === 'failed' && !buildUnsupported && (
            <section className="card card-error">
              <h2 className="h-serif">
                {setupError?.code === 'busy' ? 'Queue is full' : "Couldn't start a session"}
              </h2>
              <p className="muted">{setupError?.message}</p>
              <div className="row">
                <button className="btn btn-primary" onClick={restart} type="button">
                  Try again
                </button>
                <Link className="btn btn-ghost" to="/">
                  Back
                </Link>
              </div>
            </section>
          )}

          {buildUnsupported && (
            <Unsupported message={build?.error ?? setupError?.message}>
              {state.logs.length > 0 && <BuildLog lines={state.logs} />}
            </Unsupported>
          )}

          {buildFailed && !buildUnsupported && (
            <section className="card card-error">
              <h2 className="h-serif">The build failed</h2>
              {build?.error && <p className="muted">{build.error}</p>}
              <BuildLog lines={state.logs} />
              <div className="row">
                <button className="btn btn-primary" onClick={restart} type="button">
                  Try again
                </button>
              </div>
            </section>
          )}

          {setup !== 'failed' && !buildFailed && !buildUnsupported && !reachedLive && !ended && (
            <section className="build">
              <div className="build-head">
                <span className="muted">{state.phaseMessage || 'Preparing your sandbox…'}</span>
                {state.session?.build?.visits ? (
                  <span className="muted">{state.session.build.visits} visits</span>
                ) : null}
              </div>
              <BuildLog
                lines={state.logs}
                fill
                placeholder={
                  state.buildStatus === 'ready'
                    ? 'already built — restoring a fresh sandbox from the snapshot…'
                    : undefined
                }
              />
            </section>
          )}

          {reachedLive && (
            <>
              <details className="howran">
                <summary>
                  How we ran it <span aria-hidden="true">▸</span>
                </summary>
                <div className="howran-body">
                  <Recipe recipe={recipe} />
                  <BuildLog lines={state.logs} placeholder="no build output was recorded" />
                </div>
              </details>
              {state.previewUrl ? (
                <Preview url={state.previewUrl} />
              ) : (
                <section className="card">
                  <h2 className="h-serif">This repo runs in a terminal</h2>
                  <p className="muted">Use the shell below; it is attached to the live sandbox.</p>
                </section>
              )}
            </>
          )}

          {ended && (
            <div className="overlay">
              <div className="card overlay-card">
                <h2 className="h-serif">Session ended{state.endedReason ? ` (${state.endedReason})` : ''}</h2>
                <p className="muted">Every visitor gets a fresh restore of the same build.</p>
                <button className="btn btn-primary" onClick={restart} type="button">
                  Start again →
                </button>
              </div>
            </div>
          )}
        </main>
        <Funnel />
      </div>

      <section className={`drawer${drawerOpen ? ' drawer-open' : ''}`}>
        <button
          className="drawer-toggle mono"
          onClick={() => setDrawerOpen((o) => !o)}
          type="button"
          aria-expanded={drawerOpen}
        >
          <span>
            Terminal
            {state.terminalReady && !ended && <span className="dot-live" aria-label="ready" />}
          </span>
          <span aria-hidden="true">{drawerOpen ? '▾' : '▴'}</span>
        </button>
        <div className="drawer-body" hidden={!drawerOpen}>
          <Terminal ptyUrl={ptyUrl} connect={connectTerminal} visible={drawerOpen} />
        </div>
      </section>

      <Toast items={toasts} onDismiss={dismiss} />
    </div>
  )
}
