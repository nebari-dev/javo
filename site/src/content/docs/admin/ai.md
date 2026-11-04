---
title: AI
description: Connect a model for Explain more, Describe it, Ask AI and the admin's cluster question. MCP for coding agents needs no model.
sidebar:
  order: 9
---

Model features are off by default: nothing connects to a model and no AI control shows. The rules-based diagnosis of failures, plans and [MCP](/using/ask-ai/#coding-agents-mcp) work either way.

## Turn it on

```yaml
config:
  ai:
    enabled: true
    provider: anthropic        # or openai, for any OpenAI-compatible server
    model: claude-opus-5-5
    baseURL: ""                # for openai: such as http://vllm.example.com:8000/v1
    timeout: 60s
    features: {explain: true, draft: true, palette: true, admin: true}
    dailyTokensPerUser: 200000 # model tokens per person per UTC day
    maxInputRunes: 24000       # the most evidence sent in one call
    admin: {includeOwners: false}

ai:
  apiKeySecret:
    name: javo-ai
    key: api-key
```

```sh
kubectl -n javo-system create secret generic javo-ai --from-file=api-key=./api-key
```

- `provider: openai` covers any OpenAI-compatible server: OpenAI, Azure OpenAI, vLLM, Ollama, llama.cpp. Set `baseURL`.
- The API key comes only from the Secret named in `ai.apiKeySecret`, never from the values, and only the gateway gets it.
- `features` turns each feature on or off: `explain` (Explain more on a failure), `draft` (Describe it on New app), `palette` (Ask AI in the search palette), `admin` (Ask about this cluster, for platform admins).
- `admin.includeOwners` names app owners in what the admin question sends to the model.
- The daily allowance counts every token a call really uses: a call that uses more than it reserved (a retried answer, a longer reply) is charged the difference.
- When a platform admin explains another person's app, the fixes it proposes act on that person's app, and approving re-checks that the admin may still manage it. The admin does not see what was sent (only the owner does), and the owner's secrets go to the model as `[redacted]` without their names.

## The coding agent's model

With AI on and an OpenAI-compatible provider, coding-agent apps (the pi template, whose `agentModel` is set) get a model through the gateway, and the search bar offers **Build with pi** next to Ask AI.

```yaml
config:
  ai:
    enabled: true
    provider: openai
    baseURL: https://openrouter.ai/api/v1
    model: anthropic/claude-sonnet-5.5
    dailyTokensPerUser: 5000000  # an agent resends its conversation on every call
    agent:
      enabled: true      # the default while AI is on
      model: ""          # empty: ai.model
      models: []         # more models an agent may ask for; any other is replaced by model
      template: pi       # what Build with pi starts
      maxRequestBytes: 8388608
```

- The gateway serves `/api/ai/openai/v1/chat/completions` (streamed answers too) and `/api/ai/openai/v1/models`. Only the own API token of an app whose template sets `agentModel` may call it: not personal tokens, not other apps, not the browser. It forwards with the key from `ai.apiKeySecret`, which never enters a pod, on the agent model, counted against the person's `dailyTokensPerUser` like the other AI features (refused with a clear message when spent). Prompts and answers are never logged; each call logs the person, the model and the token counts.
- `provider: anthropic` does not serve agents: requests pass through in the OpenAI format. Use an OpenAI-compatible endpoint (OpenRouter serves Anthropic models).
- Without an agent model, each person may store their own OpenAI-compatible address, model and key in **Settings > Secrets > AI agent**. The key is sealed like a secret (`JAVO_AGENT_KEY`), shown again only by its last four characters, and given only to that person's coding-agent apps. With neither, pi asks the person to sign in (`/login`). A change applies when the agent app restarts.
- A coding agent's app token (templates with `agentDeploy`) may create, change, restart and delete its owner's apps and jobs that hold no secret, API token or Nebi token. Secrets, API access, sharing and anything on an app or job that holds a secret are refused; the agent asks the person or proposes a plan.

## MCP for coding agents

```yaml
config:
  mcp:
    enabled: true      # the default
    publicURL: https://javo.example.com   # for approve links; empty: https://<urls.uiHost>
  plans:
    ttl: 10m           # how long a proposed change waits for approval
```

Agents reach `/mcp` with a personal API token. They can read and propose changes; a person approves every change in the browser.
