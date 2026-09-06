/** badgeMarkdown renders the README snippet for a repo, pointing at this deployment's origin. */
export function badgeMarkdown(origin: string, owner: string, repo: string): string {
  const base = origin.replace(/\/+$/, '')
  return `[![Try it live](${base}/badge/gh/${owner}/${repo}.svg)](${base}/gh/${owner}/${repo})`
}
