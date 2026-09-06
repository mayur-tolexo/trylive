import type { ReactNode } from 'react'

const EXAMPLE = `# trylive.yaml
kind: web
install:
  - npm ci
start: npm run dev -- --host --port 3000
port: 3000`

/** Unsupported explains that no recipe was detected and shows how to declare one. */
export function Unsupported({ message, children }: { message?: string; children?: ReactNode }) {
  return (
    <section className="card card-error">
      <h2 className="h-serif">We couldn't work out how to run this repo yet</h2>
      {message && <p className="muted">{message}</p>}
      <p>
        Add a <code className="mono">trylive.yaml</code> to the repo root telling us how to install and start it:
      </p>
      <pre className="mono snippet-block">{EXAMPLE}</pre>
      {children}
    </section>
  )
}
