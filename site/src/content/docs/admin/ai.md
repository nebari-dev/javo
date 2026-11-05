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
- The daily allowance counts every token a call really uses. Each attempt (a call and its one retry) first reserves the most it can cost (a token per byte sent, the provider's framing and the whole output allowance) and is not sent when that does not fit in what is left, so a call never takes the day past the allowance. What an attempt did not use is given back.
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
    agent:
      enabled: true      # the default while AI is on
      model: ""          # empty: ai.model
      models: []         # more models an agent may ask for; any other is replaced by model
      template: pi       # what Build with pi starts
      maxRequestBytes: 8388608
```

- The gateway serves `/api/ai/openai/v1/chat/completions` (streamed answers too) and `/api/ai/openai/v1/models`. Only the own API token of an app whose template sets `agentModel` may call it: not personal tokens, not other apps, not the browser. It forwards with the key from `ai.apiKeySecret`, which never enters a pod, on the agent model. Only the OpenAI chat completions fields an agent needs go to the provider (messages, model, stream, sampling, stop, tools, tool_choice, response_format, seed, user and the output limit); others, such as `n` or a provider's routing and plugin fields, are dropped. The output limit is at most 32000 tokens, and 32000 when the request names none. Taking the template away from the app's owner takes the endpoint and the deploy rights away from the app at once. `dailyTokensPerUser` does not apply to it: cap the spend at the provider (such as a credit limit on the OpenRouter key). Prompts and answers are never logged; each call logs the person, the model and the token counts.
- While the agent model is on, New app's **Describe it** box suggests whole descriptions as the person types (after a short pause, from three characters), like a search box. Each suggestion call goes to the agent model with the person's templates and the names at the top of their home folder (names only, at most 40, never file contents), asks for a short answer (at most 600 output tokens) and waits at most 10 seconds. Unlike the agent's own calls, it counts against `dailyTokensPerUser`. A model that fails or an allowance that is used up shows no suggestions. Characters a person cannot see (zero-width, bidirectional, tag and other format or control characters) are taken out of each suggestion and of the first prompt an agent gets. The typed text is never logged; each call logs one `ai.call` line with `feature=suggest`, the person and the token counts.
- `provider: anthropic` does not serve agents: requests pass through in the OpenAI format. Use an OpenAI-compatible endpoint (OpenRouter serves Anthropic models).
- Without an agent model, each person may store their own OpenAI-compatible address, model and key in **Settings > Secrets > AI agent**. The key is sealed like a secret (`JAVO_AGENT_KEY`), shown again only by its last four characters, and given only to that person's coding-agent apps, which are never shared (a template with `agentModel` needs `maxSharing: Private`). On every start the pi template rewrites the `javo` provider in pi's `models.json` and drops any other provider's or stored credential's reference to `JAVO_AGENT_KEY`, so a planted file cannot send the key to another server; other providers the person added are kept. With neither, pi asks the person to sign in (`/login`). A change applies when the agent app restarts.
- A coding agent's app token (templates with `agentDeploy`) may create, change, restart and delete its owner's apps and jobs that hold no secret, API token, Nebi token or model key. Secrets, API access, sharing and anything on an app or job that holds a secret are refused; the agent asks the person or proposes a plan.

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
