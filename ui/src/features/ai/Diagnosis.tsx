import type { ReactNode } from 'react'
import { useAiFeature, useDiagnosis } from './ai'
import type { Step } from '../../api'
import { AiCard } from './AiCard'
import { FailureCard, type FailureCardProps } from '../logs/FailureCard'
import { Proposals } from './Proposals'

/**
 * The failure card of a failed app: the rules' finer cause when they found
 * one (else the failing step's own text), its evidence, and the fixes it
 * proposes. Under it, when AI is on, the model's reading of the same evidence.
 */
export function DiagnosedFailure({
  name,
  problem,
  action,
  log,
  tone,
}: { name: string; problem: Step; action?: ReactNode } & Pick<FailureCardProps, 'log' | 'tone'>) {
  const d = useDiagnosis(name, true).data
  const explain = useAiFeature('explain')
  // An unknown cause keeps the step's own words; a finer one replaces them.
  const finer = d && d.code !== 'Unknown' && d.title ? d : undefined
  const copy = finer ?? problem
  const evidence = finer ? (finer.evidence ?? []).map((e) => e.excerpt) : (problem.evidence ?? [])
  return (
    <>
      <FailureCard
        title={copy.title}
        what={copy.what}
        todo={copy.todo}
        blame={copy.blame}
        who={copy.who}
        evidence={evidence}
        log={log}
        tone={tone}
        action={
          (action || (d?.proposals?.length ?? 0) > 0) && (
            <>
              {action}
              {d && <Proposals items={d.proposals} source="rules" />}
            </>
          )
        }
      />
      {explain && d && <AiCard name={name} known={d.known} bundle={d.bundle ?? []} />}
    </>
  )
}
