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

## MCP for coding agents

```yaml
config:
  mcp:
    enabled: true      # the default
    publicURL: https://javo.example.com   # for approve links; empty gives relative links
  plans:
    ttl: 10m           # how long a proposed change waits for approval
```

Agents reach `/mcp` with a personal API token. They can read and propose changes; a person approves every change in the browser.
