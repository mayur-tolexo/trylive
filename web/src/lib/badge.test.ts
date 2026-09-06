import { badgeMarkdown } from './badge'

describe('badgeMarkdown', () => {
  it('renders the README snippet against the given origin', () => {
    expect(badgeMarkdown('https://trylive.dev/', 'o', 'r')).toBe(
      '[![Try it live](https://trylive.dev/badge/gh/o/r.svg)](https://trylive.dev/gh/o/r)',
    )
  })
})
