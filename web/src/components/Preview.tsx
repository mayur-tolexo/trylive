/** Preview embeds the running app and offers the raw URL plus a new-tab link. */
export function Preview({ url }: { url: string }) {
  return (
    <div className="preview">
      <div className="preview-bar">
        <span className="preview-url mono" title={url}>
          {url}
        </span>
        <a className="btn btn-ghost btn-sm" href={url} target="_blank" rel="noopener noreferrer">
          Open in new tab ↗
        </a>
      </div>
      <iframe
        className="preview-frame"
        src={url}
        title="live preview"
        sandbox="allow-scripts allow-forms allow-same-origin allow-popups"
        allow="clipboard-read; clipboard-write"
      />
    </div>
  )
}
