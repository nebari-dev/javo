import type { FeatureModule } from '..'
import { DevicePage } from './DevicePage'

/** The page an agent's login link opens. It has no nav entry: people arrive from the agent. */
export const deviceFeature: FeatureModule = {
  id: 'device',
  routes: [{ path: 'device', element: <DevicePage /> }],
}
