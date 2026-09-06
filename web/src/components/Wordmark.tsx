import { Link } from 'react-router-dom'

/** Wordmark is the "trylive" mark with its green play glyph, linking home. */
export function Wordmark({ compact = false }: { compact?: boolean }) {
  return (
    <Link to="/" className={`wordmark${compact ? ' wordmark-compact' : ''}`} aria-label="trylive home">
      <span className="wordmark-mark" aria-hidden="true">
        ▶
      </span>
      <span className="wordmark-text">trylive</span>
    </Link>
  )
}
