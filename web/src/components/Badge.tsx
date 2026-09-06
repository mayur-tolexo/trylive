import { useMemo, useState } from 'react'
import { badgeMarkdown } from '../lib/badge'
import { copyText } from '../lib/clipboard'
import { parseRepo } from '../lib/repo'

/** Badge lets a maintainer preview their README badge and copy the markdown for it. */
export function Badge({ initial = '' }: { initial?: string }) {
  const [input, setInput] = useState(initial)
  const [copied, setCopied] = useState(false)
  const ref = useMemo(() => parseRepo(input), [input])
  const origin = window.location.origin
  const md = ref ? badgeMarkdown(origin, ref.owner, ref.repo) : ''

  const copy = async () => {
    if (!md) return
    if (await copyText(md)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    }
  }

  return (
    <div className="badge-tool">
      <label className="field">
        <span className="field-label">Your repo</span>
        <input
          className="input mono"
          placeholder="owner/repo"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          spellCheck={false}
          autoCapitalize="off"
        />
      </label>
      {ref && (
        <>
          <div className="badge-preview">
            <img
              src={`/badge/gh/${ref.owner}/${ref.repo}.svg`}
              alt="Try it live badge"
              height={20}
            />
            <span className="muted">← this is what lands in your README</span>
          </div>
          <div className="snippet">
            <code className="mono">{md}</code>
            <button className="btn btn-ghost btn-sm" onClick={copy} type="button">
              {copied ? 'Copied' : 'Copy markdown'}
            </button>
          </div>
        </>
      )}
    </div>
  )
}
