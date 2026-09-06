/** A GitHub repository reference, already normalised to its owner and name. */
export interface RepoRef {
  owner: string
  repo: string
}

// GitHub user/org and repo names: alphanumerics, dashes, dots, underscores.
const SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._-]*$/

/**
 * parseRepo accepts a bare `owner/repo` slug or any github.com URL (with or
 * without scheme, `.git`, trailing slash, or `/tree/<branch>/...` suffix) and
 * returns the owner/repo pair, or null when the input is not a GitHub repo.
 */
export function parseRepo(input: string): RepoRef | null {
  const raw = input.trim()
  if (!raw) return null

  let path: string
  if (/^(https?:\/\/|git@|github\.com\/)/i.test(raw)) {
    // Fold the SSH form into an URL-ish path, then strip host and scheme.
    const normalised = raw
      .replace(/^git@github\.com:/i, 'github.com/')
      .replace(/^https?:\/\//i, '')
      .replace(/^www\./i, '')
    if (!/^github\.com\//i.test(normalised)) return null
    path = normalised.slice('github.com/'.length)
  } else if (raw.includes('://') || raw.includes('@')) {
    return null
  } else {
    path = raw
  }

  const parts = path.split(/[?#]/)[0].split('/').filter(Boolean)
  if (parts.length < 2) return null
  const [owner, repoRaw] = parts
  const repo = repoRaw.replace(/\.git$/i, '')
  if (!SEGMENT.test(owner) || !SEGMENT.test(repo)) return null
  if (repo === '.' || repo === '..') return null
  // A bare slug must be exactly two segments; URLs may carry tree/blob paths.
  if (path === raw && parts.length !== 2) return null
  return { owner, repo }
}

/** slug renders a RepoRef as `owner/repo`. */
export function slug(ref: RepoRef): string {
  return `${ref.owner}/${ref.repo}`
}
