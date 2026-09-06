import { useEffect, useRef, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { openPtySocket, type PtySocket } from './ptySocket'

/** Props for the Terminal: how to build the pty URL for a given size, and whether to connect. */
export interface TerminalProps {
  ptyUrl: (cols: number, rows: number) => string
  /** When false the pane renders but no socket is opened (build still running, session ended). */
  connect: boolean
  /** Rendering size only changes when visible; hidden drawers must not fit to 0x0. */
  visible: boolean
}

const DARK = {
  background: '#0F1115',
  foreground: '#E8EAF0',
  cursor: '#3DDC84',
  selectionBackground: '#3DDC8433',
  black: '#0F1115',
  brightBlack: '#9AA3B5',
  green: '#3DDC84',
  brightGreen: '#3DDC84',
  red: '#FF6B6B',
  brightRed: '#FF6B6B',
}

const LIGHT = {
  ...DARK,
  background: '#FFFFFF',
  foreground: '#14161B',
  black: '#14161B',
  brightBlack: '#5B6475',
  selectionBackground: '#3DDC8444',
}

/**
 * Terminal mounts xterm.js and bridges it to the pty websocket: keystrokes go
 * out as bytes, server bytes are written to the screen, and every fit sends a
 * resize frame. An `exit` control frame disables input and prints a notice.
 */
export function Terminal({ ptyUrl, connect, visible }: TerminalProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<XTerm | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const sockRef = useRef<PtySocket | null>(null)
  const [status, setStatus] = useState<'idle' | 'connecting' | 'open' | 'exited' | 'closed'>('idle')

  // Flow 1: create the xterm instance once, sized to its host by FitAddon.
  useEffect(() => {
    const host = hostRef.current
    if (!host) return
    const dark = window.matchMedia('(prefers-color-scheme: dark)').matches
    const term = new XTerm({
      fontFamily: '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.2,
      cursorBlink: true,
      scrollback: 5000,
      theme: dark ? DARK : LIGHT,
      allowProposedApi: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)
    termRef.current = term
    fitRef.current = fit

    const ro = new ResizeObserver(() => {
      if (host.clientHeight === 0 || host.clientWidth === 0) return
      try {
        fit.fit()
      } catch {
        // xterm throws when its renderer is not ready; the next observation retries.
      }
      sockRef.current?.resize(term.cols, term.rows)
    })
    ro.observe(host)
    return () => {
      ro.disconnect()
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
  }, [])

  // Re-fit when the drawer opens, since a hidden host reports a 0x0 box.
  useEffect(() => {
    if (!visible) return
    const raf = requestAnimationFrame(() => {
      try {
        fitRef.current?.fit()
      } catch {
        // See above: renderer not ready yet.
      }
      const t = termRef.current
      if (t) sockRef.current?.resize(t.cols, t.rows)
    })
    return () => cancelAnimationFrame(raf)
  }, [visible])

  // Flow 2: open the socket while `connect` holds; tear it down otherwise.
  useEffect(() => {
    const term = termRef.current
    if (!connect || !term) return
    setStatus('connecting')
    term.reset()
    term.options.disableStdin = false
    const sock = openPtySocket(ptyUrl(term.cols, term.rows), {
      onOpen: () => {
        setStatus('open')
        sock.resize(term.cols, term.rows)
        term.focus()
      },
      onData: (bytes) => term.write(bytes),
      onExit: (frame) => {
        setStatus('exited')
        term.options.disableStdin = true
        const code = frame.code !== undefined ? ` (code ${frame.code})` : ''
        term.write(`\r\n\x1b[90m— process exited${code} —\x1b[0m\r\n`)
      },
      onClose: () => setStatus((s) => (s === 'exited' ? s : 'closed')),
      onError: () => setStatus((s) => (s === 'exited' ? s : 'closed')),
    })
    sockRef.current = sock
    const sub = term.onData((d) => sock.send(d))
    const subBin = term.onBinary((d) => sock.send(Uint8Array.from(d, (c) => c.charCodeAt(0))))
    return () => {
      sub.dispose()
      subBin.dispose()
      sock.close()
      sockRef.current = null
    }
  }, [connect, ptyUrl])

  return (
    <div className="term">
      <div ref={hostRef} className="term-host" />
      {status !== 'open' && (
        <div className="term-overlay mono muted" aria-live="polite">
          {status === 'connecting' && 'connecting to the sandbox shell…'}
          {status === 'idle' && (connect ? '' : 'the shell becomes available once the sandbox is live')}
          {status === 'exited' && 'process exited'}
          {status === 'closed' && 'connection closed'}
        </div>
      )}
    </div>
  )
}
