import { Card } from './Card'
import { CodeBlock } from './CodeBlock'

/** The one line a person pastes into a coding agent so it deploys the folder it works in. */
export function AgentDeploy() {
  const line = `Deploy the app in this folder to Javo. Follow ${window.location.origin}/llms.txt`
  return (
    <Card className="pad agent-deploy">
      <h2 className="card-h">Deploy from your computer</h2>
      <p className="sub">Paste this into your coding agent in the app's folder.</p>
      <CodeBlock text={line} />
    </Card>
  )
}
