import { parseRepo } from './repo'

describe('parseRepo', () => {
  const ok = { owner: 'vercel', repo: 'next.js' }
  it.each([
    ['vercel/next.js'],
    ['https://github.com/vercel/next.js'],
    ['https://github.com/vercel/next.js.git'],
    ['https://github.com/vercel/next.js/'],
    ['http://www.github.com/vercel/next.js/tree/canary/examples'],
    ['github.com/vercel/next.js/blob/main/README.md'],
    ['git@github.com:vercel/next.js.git'],
    ['  https://github.com/vercel/next.js?tab=readme  '],
  ])('parses %s', (input) => {
    expect(parseRepo(input)).toEqual(ok)
  })

  it.each([
    [''],
    ['   '],
    ['vercel'],
    ['vercel/'],
    ['/next.js'],
    ['vercel/next.js/extra'],
    ['https://gitlab.com/vercel/next.js'],
    ['https://github.com/vercel'],
    ['ftp://github.com/vercel/next.js'],
    ['owner/re po'],
    ['-bad/repo'],
  ])('rejects %j', (input) => {
    expect(parseRepo(input)).toBeNull()
  })
})
