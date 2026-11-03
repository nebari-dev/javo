---
title: Ask AI and coding agents
description: Model help in the web UI when your admin turns it on, and coding agents that work through MCP with your approval.
sidebar:
  order: 7
---

## Ask AI

Model features are off until an admin [connects a model](/admin/ai/). With them off, no AI control shows anywhere. When they are on, you may see:

- **Explain more** on a failure: the rules-based diagnosis of a failed app or job is always there; this asks the model to explain it further.
- **Describe it** on New app: describe the app you want in plain words and the model fills in the form. Nothing is created until you press **Create app**.
- **Ask AI** in the search palette (Cmd+K or Ctrl+K): ask for a change. The answer is a plan you approve, or a draft that opens New app.
- **Ask about this cluster** in the palette, for platform admins: questions about the cluster report. The answer cites its rows and changes nothing.

Each person has a daily allowance of model tokens (per UTC day). When it is used up, Javo says when it resets.

## Coding agents (MCP)

Agents such as Claude Code or Cursor can work with Javo through its MCP endpoint at `/mcp` on the Javo address. MCP needs no model and is on unless your admin turned it off.

1. Create a [personal API token](/using/api/#api-tokens).
2. Configure the agent with the MCP address, `https://<javo address>/mcp`, and the token as a bearer token.

An agent can read what you can read. It can only **propose** changes: each proposal is a plan that waits for you to approve or reject it in the browser, at a link the agent shows you. Approving needs your browser session; a token alone can never approve. Plans expire after 10 minutes unless your admin changed it.
