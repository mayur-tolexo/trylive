/** Control messages the browser sends to the pty endpoint as text frames. */
export interface ResizeFrame {
  type: 'resize'
  cols: number
  rows: number
}

/** Control messages the server sends as text frames; only `exit` is defined today. */
export interface ExitFrame {
  type: 'exit'
  code?: number
}

/** encodeResize builds the text frame announcing a new terminal size. */
export function encodeResize(cols: number, rows: number): string {
  const frame: ResizeFrame = {
    type: 'resize',
    cols: Math.max(1, Math.floor(cols)),
    rows: Math.max(1, Math.floor(rows)),
  }
  return JSON.stringify(frame)
}

/** parseControl decodes a text frame from the server; null for anything not an exit frame. */
export function parseControl(text: string): ExitFrame | null {
  let v: unknown
  try {
    v = JSON.parse(text)
  } catch {
    return null
  }
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  if (o.type !== 'exit') return null
  return typeof o.code === 'number' ? { type: 'exit', code: o.code } : { type: 'exit' }
}

/** toBytes normalises a binary WebSocket payload (ArrayBuffer or view) to a Uint8Array. */
export function toBytes(data: ArrayBuffer | ArrayBufferView): Uint8Array {
  if (data instanceof ArrayBuffer) return new Uint8Array(data)
  return new Uint8Array(data.buffer, data.byteOffset, data.byteLength)
}

/** Callbacks a PtySocket reports to; all optional so callers wire only what they need. */
export interface PtyHandlers {
  onData: (bytes: Uint8Array) => void
  onExit?: (frame: ExitFrame) => void
  onOpen?: () => void
  onClose?: () => void
  onError?: () => void
}

/** Thin wrapper over a WebSocket speaking the pty protocol: bytes both ways, JSON control frames. */
export interface PtySocket {
  send(bytes: Uint8Array | string): void
  resize(cols: number, rows: number): void
  close(): void
}

/**
 * openPtySocket connects to `url` and routes frames: binary payloads are
 * terminal bytes, text payloads are control frames. Input typed by the user
 * is encoded to UTF-8 and sent as binary.
 */
export function openPtySocket(url: string, handlers: PtyHandlers): PtySocket {
  const ws = new WebSocket(url)
  ws.binaryType = 'arraybuffer'
  const encoder = new TextEncoder()

  ws.onopen = () => handlers.onOpen?.()
  ws.onclose = () => handlers.onClose?.()
  ws.onerror = () => handlers.onError?.()
  ws.onmessage = (ev: MessageEvent) => {
    if (typeof ev.data === 'string') {
      const ctl = parseControl(ev.data)
      if (ctl) handlers.onExit?.(ctl)
      return
    }
    handlers.onData(toBytes(ev.data as ArrayBuffer))
  }

  const ready = () => ws.readyState === WebSocket.OPEN
  return {
    send: (bytes) => {
      if (!ready()) return
      ws.send(typeof bytes === 'string' ? encoder.encode(bytes) : bytes)
    },
    resize: (cols, rows) => {
      if (ready()) ws.send(encodeResize(cols, rows))
    },
    close: () => ws.close(),
  }
}
