import { useEffect, useState } from 'react'

/** formatRemaining renders whole seconds as mm:ss, clamping at 00:00. */
export function formatRemaining(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const m = Math.floor(total / 60)
  const s = total % 60
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
}

/** Countdown ticks once a second toward `expiresAt` and turns red under a minute. */
export function Countdown({ expiresAt }: { expiresAt?: string }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  if (!expiresAt) return null
  const remaining = new Date(expiresAt).getTime() - now
  if (Number.isNaN(remaining)) return null
  const low = remaining < 60_000
  return (
    <span className={`countdown mono${low ? ' countdown-low' : ''}`} title="time left in this sandbox">
      {formatRemaining(remaining)}
    </span>
  )
}
