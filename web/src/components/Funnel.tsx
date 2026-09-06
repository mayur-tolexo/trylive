/** Funnel is the persistent NeevCloud strip shown beside a running sandbox. */
export function Funnel() {
  return (
    <aside className="funnel">
      <a className="funnel-cta" href="https://neevcloud.com" target="_blank" rel="noopener noreferrer">
        This is a live NeevCloud sandbox — get one of your own →
      </a>
      <span className="muted funnel-sub">Built once, restored in ~3s for every visitor.</span>
    </aside>
  )
}
