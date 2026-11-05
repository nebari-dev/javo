import { describe, expect, it } from 'vitest'
import { homeFileLink } from './homeLinks'

describe('homeFileLink', () => {
  it('opens a file of the home in the file view', () => {
    expect(homeFileLink('/home/user/agents/nebi-dashboard/app.py')).toBe('/files/view?path=agents%2Fnebi-dashboard%2Fapp.py')
  })

  it('opens a folder of the home in the file browser', () => {
    expect(homeFileLink('/home/user/agents/data/')).toBe('/files?path=agents%2Fdata')
    expect(homeFileLink('/home/user/agents/data', 'folder')).toBe('/files?path=agents%2Fdata')
    expect(homeFileLink('/home/user')).toBe('/files')
    expect(homeFileLink('/home/user/')).toBe('/files')
  })

  it('does not link a path outside the home', () => {
    expect(homeFileLink('/opt/javo/pod-user.sh')).toBeNull()
    expect(homeFileLink('/home/username/a.py')).toBeNull()
    expect(homeFileLink('/home/user/../etc/passwd')).toBeNull()
    expect(homeFileLink('sales/app.py')).toBeNull()
  })
})
