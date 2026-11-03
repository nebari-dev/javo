import type { Row } from '../../../api'
import { Banner } from '../../../components/Banner'
import { More } from '../../../components/More'
import { problemText } from '../../../lib/http'
import { useAdminStorage } from '../../../lib/queries'

/** One thing that limits people or needs an admin, with its technical names folded away. */
function Attention({ row }: { row: Row }) {
  const n = row.notice
  const details = [n.detail, ...(row.evidence ?? [])].filter(Boolean)
  return (
    <Banner tone={n.tone === 'err' || n.tone === 'warn' ? n.tone : 'info'} title={n.title}>
      <p className="tight">{n.text}</p>
      <p className="tight">{n.action}</p>
      {details.length > 0 && (
        <More closed="Details" open="Hide details">
          <pre className="evidence">{details.join('\n')}</pre>
        </More>
      )}
    </Banner>
  )
}

/**
 * Settings, Storage: where people's files are, and only what needs an admin.
 * Storage is set when the platform is deployed, so the page only reports.
 */
export function Storage() {
  const report = useAdminStorage()
  if (report.isError) return <Banner tone="err" title={problemText(report.error)} />
  if (report.isPending) return <div className="muted">Loading</div>
  const rows = (report.data.rows ?? []).filter((r) => r.attention)
  return (
    <>
      {report.data.summary && <p className="sub tight">{report.data.summary}</p>}
      {rows.length > 0 && (
        <div className="section-sm stack">
          {rows.map((r, i) => (
            <Attention key={`${r.fact}-${i}`} row={r} />
          ))}
        </div>
      )}
    </>
  )
}
