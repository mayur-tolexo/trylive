import type { BuildStatus, SessionStatus } from '../api/types'
import { Countdown } from './Countdown'
import { StatusPill } from './StatusPill'
import { Wordmark } from './Wordmark'

/** Everything the session top bar needs to render its identity, status and actions. */
export interface TopBarProps {
  owner: string
  repo: string
  sha?: string
  sessionStatus: SessionStatus
  buildStatus: BuildStatus
  expiresAt?: string
  extended: boolean
  onExtend: () => void
  onCopyBadge: () => void
  onShare: () => void
}

/** TopBar: wordmark, repo link, short sha, status, TTL, and the three session actions. */
export function TopBar(p: TopBarProps) {
  const canExtend = !p.extended && p.sessionStatus !== 'ended' && Boolean(p.expiresAt)
  return (
    <header className="topbar">
      <div className="topbar-left">
        <Wordmark compact />
        <span className="topbar-sep" aria-hidden="true">
          →
        </span>
        <a
          className="topbar-repo mono"
          href={`https://github.com/${p.owner}/${p.repo}`}
          target="_blank"
          rel="noopener noreferrer"
        >
          {p.owner}/{p.repo}
        </a>
        {p.sha && (
          <span className="topbar-sha mono muted" title={p.sha}>
            {p.sha.slice(0, 7)}
          </span>
        )}
        <StatusPill session={p.sessionStatus} build={p.buildStatus} />
        {p.sessionStatus !== 'ended' && <Countdown expiresAt={p.expiresAt} />}
      </div>
      <div className="topbar-right">
        <button className="btn btn-ghost btn-sm" onClick={p.onExtend} disabled={!canExtend} type="button">
          {p.extended ? 'Extended' : 'Keep going +15 min'}
        </button>
        <button className="btn btn-ghost btn-sm" onClick={p.onCopyBadge} type="button">
          Copy badge
        </button>
        <button className="btn btn-ghost btn-sm" onClick={p.onShare} type="button">
          Share
        </button>
      </div>
    </header>
  )
}
