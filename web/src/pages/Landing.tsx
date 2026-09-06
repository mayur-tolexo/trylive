import { useCallback, useMemo, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { createClient, isApiError } from '../api/client'
import { Badge } from '../components/Badge'
import { Toast, type ToastMessage } from '../components/Toast'
import { Unsupported } from '../components/Unsupported'
import { Wordmark } from '../components/Wordmark'
import { parseRepo } from '../lib/repo'

/** Repos offered as one-click examples on the landing page. */
const EXAMPLES = ['heroku/node-js-getting-started', 'streamlit/streamlit-example', 'sveltejs/template']

/**
 * Landing: one input, one button. Submitting creates the session here so the
 * visitor lands on /gh/owner/repo?s=<id> with the stream already attached.
 */
export function Landing() {
  const navigate = useNavigate()
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [unsupported, setUnsupported] = useState<string | null>(null)
  const [toasts, setToasts] = useState<ToastMessage[]>([])
  const dismiss = useCallback((id: number) => setToasts((t) => t.filter((x) => x.id !== id)), [])
  const toast = (text: string, tone: ToastMessage['tone'] = 'info') =>
    setToasts((t) => [...t, { id: Date.now() + Math.random(), text, tone }])

  const client = useMemo(
    () =>
      createClient({
        onBusyRetry: (attempt, delayMs) =>
          setBusy(`Queue is full, retrying in ${delayMs / 1000}s (${attempt}/3)…`),
      }),
    [],
  )

  const submit = async (raw: string) => {
    setError(null)
    setUnsupported(null)
    const ref = parseRepo(raw)
    if (!ref) {
      setError('Enter a GitHub repo as owner/repo or a github.com URL.')
      return
    }
    setBusy('Starting your sandbox…')
    try {
      const s = await client.createSession(`${ref.owner}/${ref.repo}`)
      navigate(`/gh/${ref.owner}/${ref.repo}?s=${encodeURIComponent(s.id)}`)
    } catch (err) {
      setBusy(null)
      if (!isApiError(err)) {
        setError('Something went wrong. Please try again.')
        return
      }
      switch (err.code) {
        case 'unsupported':
          setUnsupported(err.message)
          break
        case 'rate_limited':
          toast(
            `Too many sessions right now. Try again in ${err.retryAfterSeconds ?? 60}s.`,
            'danger',
          )
          break
        case 'busy':
          setError('The queue is still full. Please try again in a minute.')
          break
        case 'invalid':
        case 'not_found':
          setError(err.message || 'That repo could not be found on GitHub.')
          break
        default:
          setError(err.message || 'Something went wrong. Please try again.')
      }
    }
  }

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    void submit(value)
  }

  return (
    <div className="landing">
      <header className="landing-nav">
        <Wordmark />
        <a className="muted" href="#badge">
          Add the badge
        </a>
      </header>

      <main className="landing-main">
        <section className="hero">
          <h1 className="h-serif hero-title">
            Run any GitHub repo in your browser.
            <br />
            No install, no login.
          </h1>
          <form className="hero-form" onSubmit={onSubmit}>
            <input
              className="input input-lg mono"
              placeholder="owner/repo or https://github.com/owner/repo"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              disabled={busy !== null}
              autoFocus
              spellCheck={false}
              autoCapitalize="off"
              aria-label="GitHub repository"
            />
            <button className="btn btn-primary btn-lg" type="submit" disabled={busy !== null}>
              {busy ? 'Starting…' : 'Try it live'}
            </button>
          </form>
          {busy && <p className="muted hero-note" aria-live="polite">{busy}</p>}
          {error && <p className="danger hero-note" role="alert">{error}</p>}
          <div className="chips">
            <span className="muted">Try:</span>
            {EXAMPLES.map((ex) => (
              <button
                key={ex}
                className="chip mono"
                type="button"
                disabled={busy !== null}
                onClick={() => {
                  setValue(ex)
                  void submit(ex)
                }}
              >
                {ex}
              </button>
            ))}
          </div>
          {unsupported && <Unsupported message={unsupported} />}
        </section>

        <section className="section" id="badge">
          <h2 className="h-serif">Add the badge to your README</h2>
          <p className="muted">
            Visitors click it and get a running copy of your repo in a few seconds. You build once; every
            visitor gets a fresh restore.
          </p>
          <Badge />
        </section>
      </main>

      <footer className="footer">
        <span className="muted">
          Sandboxes by{' '}
          <a href="https://neevcloud.com" target="_blank" rel="noopener noreferrer">
            NeevCloud
          </a>
        </span>
      </footer>
      <Toast items={toasts} onDismiss={dismiss} />
    </div>
  )
}
