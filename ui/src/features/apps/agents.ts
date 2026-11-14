// Coding agents by kind, with the name people know them by.
export const AGENTS: Record<string, string> = { pi: 'Pi', 'claude-code': 'Claude Code', herdr: 'herdr' }

/** The agent's name as people know it ("Pi"), or "This agent" for a kind without one. */
export const agentOf = (ws: { kind: string }) => AGENTS[ws.kind] ?? 'This agent'
