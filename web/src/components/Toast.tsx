import { useEffect } from 'react'

/** A toast message; `tone` picks the accent colour. */
export interface ToastMessage {
  id: number
  text: string
  tone?: 'info' | 'danger'
}

/** Toast renders a stack of transient messages, dismissing each after `ttlMs`. */
export function Toast({
  items,
  onDismiss,
  ttlMs = 5000,
}: {
  items: ToastMessage[]
  onDismiss: (id: number) => void
  ttlMs?: number
}) {
  useEffect(() => {
    if (items.length === 0) return
    const timers = items.map((t) => setTimeout(() => onDismiss(t.id), ttlMs))
    return () => timers.forEach(clearTimeout)
  }, [items, onDismiss, ttlMs])

  if (items.length === 0) return null
  return (
    <div className="toasts" role="status" aria-live="polite">
      {items.map((t) => (
        <div key={t.id} className={`toast${t.tone === 'danger' ? ' toast-danger' : ''}`}>
          <span>{t.text}</span>
          <button className="toast-x" onClick={() => onDismiss(t.id)} aria-label="dismiss">
            ×
          </button>
        </div>
      ))}
    </div>
  )
}
