import { describe, expect, it } from 'vitest'
import { buildLink, sourceLink } from './github'

const sha = '0123456789abcdef0123456789abcdef01234567'

describe('buildLink', () => {
  it('names a release by its tag and links to the release', () => {
    expect(buildLink({ version: 'v0.2.22', commit: sha })).toEqual({
      label: 'v0.2.22',
      href: 'https://github.com/nebari-dev/javo/releases/tag/v0.2.22',
    })
  })
  it('names a dev build by its short commit and links to the commit', () => {
    expect(buildLink({ version: 'dev', commit: sha })).toEqual({
      label: 'dev+0123456',
      href: `https://github.com/nebari-dev/javo/commit/${sha}`,
    })
  })
  it('links a build that knows no commit to the repository', () => {
    expect(buildLink({ version: 'dev' })).toEqual({ label: 'dev', href: 'https://github.com/nebari-dev/javo' })
  })
})

describe('sourceLink', () => {
  it('points at the file in the release, the commit, or main', () => {
    const path = 'pod/files/pod-user.sh'
    expect(sourceLink({ version: 'v0.2.22', commit: sha }, path)).toBe('https://github.com/nebari-dev/javo/blob/v0.2.22/pod/files/pod-user.sh')
    expect(sourceLink({ version: 'dev', commit: sha }, path)).toBe(`https://github.com/nebari-dev/javo/blob/${sha}/pod/files/pod-user.sh`)
    expect(sourceLink(undefined, path)).toBe('https://github.com/nebari-dev/javo/blob/main/pod/files/pod-user.sh')
  })
})
