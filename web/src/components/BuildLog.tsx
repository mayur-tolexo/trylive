import { useEffect, useRef } from 'react'
import type { LogLine } from '../sse/reducer'

/** Props: the capped log lines and whether the console should fill its container. */
export interface BuildLogProps {
  lines: LogLine[]
  fill?: boolean
  /** Shown as a placeholder before the first line arrives. */
  placeholder?: string
}

/**
 * BuildLog renders build output in a monospace console. A divider is inserted
 * whenever the phase changes between consecutive lines, and the view sticks to
 * the bottom until the visitor scrolls up.
 */
export function BuildLog({ lines, fill = false, placeholder = 'waiting for the build to start…' }: BuildLogProps) {
  const ref = useRef<HTMLDivElement>(null)
  const stick = useRef(true)

  useEffect(() => {
    const el = ref.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [lines])

  const onScroll = () => {
    const el = ref.current
    if (!el) return
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }

  return (
    <div ref={ref} className={`log mono${fill ? ' log-fill' : ''}`} onScroll={onScroll} role="log" aria-live="off">
      {lines.length === 0 && <div className="log-placeholder muted">{placeholder}</div>}
      {lines.map((l, i) => {
        const newPhase = i === 0 || lines[i - 1].phase !== l.phase
        return (
          <div key={i}>
            {newPhase && l.phase && (
              <div className="log-phase" aria-label={`phase ${l.phase}`}>
                <span>{l.phase}</span>
              </div>
            )}
            <div className="log-line">{l.line}</div>
          </div>
        )
      })}
    </div>
  )
}
