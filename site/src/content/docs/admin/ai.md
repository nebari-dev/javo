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

## Change it in Settings > AI

With AI on, platform admins set an OpenAI-compatible address, the model and the API key in **Settings > AI**, without a Helm upgrade. What is saved there is used in place of `ai.provider`, `ai.baseURL`, `ai.model` and the key from `ai.apiKeySecret`; with nothing saved, the Helm values apply as they are. **Use the platform config** removes what was saved. `ai.enabled`, `features`, `timeout`, the allowance and the `agent` settings stay in Helm.

- **Provider** fills the address with a provider's OpenAI-compatible endpoint: Anthropic (Claude) `https://api.anthropic.com/v1/`, OpenAI `https://api.openai.com/v1`, OpenRouter `https://openrouter.ai/api/v1`, Google Gemini `https://generativelanguage.googleapis.com/v1beta/openai/`, Mistral `https://api.mistral.ai/v1`, Groq `https://api.groq.com/openai/v1`, Together AI `https://api.together.ai/v1`, DeepSeek `https://api.deepseek.com` or xAI `https://api.x.ai/v1`. **Custom** takes any address. **Model** lists popular models of the chosen provider (OpenRouter's carry the provider prefix, such as `anthropic/claude-sonnet-5.5`); **Custom** takes any model id. The lists live in `ui/src/features/settings/admin/aiProviders.ts`.
- Every address saved here is spoken to as an OpenAI-compatible server, also when Helm sets `provider: anthropic` (which keeps working from Helm). With such a config, the page starts at Anthropic's OpenAI-compatible address.
- A save applies to the next model call of every AI feature (Explain more, Describe it and its suggestions, Ask AI, the admin's question) and of the coding agents' endpoint, on every gateway replica, without a restart. The provider self-test runs again for the new settings. Agent apps already running keep the model mode they started with until they restart; their requests go to the new model at once.
- The key is write-only. It is stored in the database encrypted (Tink AEAD with the gateway's sessions key, from the Secret `javo-sessions-key`, bound to this use), never logged and never returned: the page shows only its last four characters. Leave the field empty to keep it while changing the rest; **Remove key** deletes it. A gateway without `--sessions-key-secret` makes a key at each start, so a saved key has to be saved again after a restart.
- The key from `ai.apiKeySecret` is sent only to the server set in Helm (same scheme, host and port; an empty `ai.baseURL` is the provider's own API, `api.anthropic.com` or `api.openai.com`). So with `provider: anthropic` in Helm, saving `https://api.anthropic.com/v1/` keeps using the Helm key. Saving another server without a key sends no key (fine for a server that needs none).
- The address must be `https` and may not name a link-local or cloud metadata address (such as 169.254.169.254 or metadata.google.internal); the gateway also refuses to connect to one, whatever a name resolves to. A plain `http` server inside the cluster stays a Helm setting. The model is at most 200 characters and the key at most 4096.
- Each save and reset is on the audit log (`GET /api/admin/audit`, actions `ai.settings.update` and `ai.settings.reset`) with who made it and which fields changed, never the key.
- Members see only the line in **Settings > Secrets > AI agent**, which names the model their coding agents use now.

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
- Without an agent model, each person may store their own OpenAI-compatible address, model and key in **Settings > Secrets > AI agent**. The key is sealed like a secret (`JAVO_AGENT_KEY`), shown again only by its last four characters, and given only to that person's coding-agent apps, which are never shared (a template with `agentModel` needs `maxSharing: Private`). In a coding-agent app (a template of category `agents`) neither this key nor the app's API token is in the agent's container: a helper sidecar of the app (`javo agent-helper`) holds them, and the agent's model calls (to `http://127.0.0.1:7690/model`) and `javo-api` calls (on the socket `/run/javo-agent/api.sock`) go through it, which adds the key or the token and reaches only the model's address and the Javo API. On every start the pi template rewrites the `javo` provider in pi's `models.json` and drops any other provider's or stored credential's reference to `JAVO_AGENT_KEY`, so a planted file cannot send the key to another server; other providers the person added are kept. With neither, pi asks the person to sign in (`/login`). A change applies when the agent app restarts.
- A coding agent's app token (templates with `agentDeploy`) may create, change, restart and delete its owner's apps and jobs that hold no secret, API token, Nebi token or model key. Secrets, API access, sharing and anything on an app or job that holds a secret are refused; the agent asks the person or proposes a plan.

## Agents that act without asking

```yaml
config:
  agents:
    allowTrusted: true   # the default; false refuses it everywhere
```

On a coding agent's app page (an agent with read-write API access), its owner can turn on **Let it act without asking** for 1 to 24 hours (8 by default). Until then the agent's own token may do whatever the owner may do with a read-write token, directly: start, stop and delete apps, attach the owner's secrets, share apps. It never acts as an admin, even for an admin, and like every token it still cannot approve or reject plans, make API tokens or change this switch. Only the owner can turn it on, signed in in the browser; no API token can. It ends by itself and the agent asks again at once; the app does not restart.

The risk is the agent's: a web page, file or package that tricks it can then act as its owner. Set `allowTrusted: false` to refuse it, or switch it off in **Settings > Agents** (platform admins): no one can turn it on and every agent acting without asking goes back to asking at once. Turning it on or off, its end, and every change the agent makes while it is on are on the audit log; those changes name the agent's app (`app:<app>`, via `trusted`) as the actor.

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
