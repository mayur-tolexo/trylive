import type { BuildStatus, SessionStatus } from '../api/types'

/** Props: the session status plus the build status, which refines the pre-live labels. */
export interface StatusPillProps {
  session: SessionStatus
  build: BuildStatus
}

/** statusLabel maps the two backend statuses to the five visitor-facing words. */
export function statusLabel(session: SessionStatus, build: BuildStatus): string {
  if (session === 'ended') return 'Ended'
  if (session === 'live') return 'Live'
  if (session === 'starting') return 'Starting'
  if (build === 'building' || session === 'building') return 'Building'
  return 'Queued'
}

/** StatusPill shows the session's lifecycle word; Live carries a pulsing green dot. */
export function StatusPill({ session, build }: StatusPillProps) {
  const label = statusLabel(session, build)
  const tone = label === 'Live' ? 'live' : label === 'Ended' ? 'ended' : 'pending'
  return (
    <span className={`pill pill-${tone}`} role="status" aria-live="polite">
      <span className="pill-dot" aria-hidden="true" />
      {label}
    </span>
  )
}
