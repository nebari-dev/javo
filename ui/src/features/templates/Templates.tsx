import { Link } from 'react-router'
import { Banner } from '../../components/Banner'
import { KindLogo } from '../../components/KindLogo'
import { TemplateGroups } from './TemplateGroups'
import { problemText } from '../../lib/http'
import { useTemplates } from '../../lib/queries'

export function Templates() {
  const templates = useTemplates()
  return (
    <>
      <h1>Templates</h1>
      <p className="sub">Each app starts from one of these.</p>
      <div className="section">
        {templates.isError ? (
          <Banner tone="err" title={problemText(templates.error)} />
        ) : (
          templates.data && (
            <TemplateGroups
              heading
              templates={templates.data}
              card={(t) => (
                <div key={t.name} className="card sample-card tpl-card">
                  <Link to={`/apps/new?template=${encodeURIComponent(t.name)}`} aria-label={`New ${t.displayName} app`}>
                    <KindLogo kind={t.kind} />
                    <span>
                      <h3>{t.displayName}</h3>
                      <span className="muted small block one-line" title={t.description}>
                        {t.description}
                      </span>
                    </span>
                  </Link>
                </div>
              )}
            />
          )
        )}
      </div>
    </>
  )
}
