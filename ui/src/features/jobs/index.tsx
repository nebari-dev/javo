import type { FeatureModule } from '..'
import { PART_ICONS } from '../../components/Section'
import { JobDetail } from './JobDetail'
import { JobsHomeSection, useJobsAttention } from './JobsHome'
import { JobsList } from './JobsList'
import { EditJob, NewJob } from './NewJob'

export const jobsFeature: FeatureModule = {
  id: 'jobs',
  routes: [
    { path: 'jobs', element: <JobsList /> },
    { path: 'jobs/new', element: <NewJob /> },
    { path: 'jobs/:name', element: <JobDetail /> },
    { path: 'jobs/:name/edit', element: <EditJob /> },
  ],
  nav: { to: '/jobs', label: 'Jobs', icon: PART_ICONS.jobs },
  homeSections: [JobsHomeSection],
  useAttention: useJobsAttention,
}
