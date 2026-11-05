/**
 * The providers Settings > AI offers, each with its OpenAI-compatible address
 * and popular models. Checked against each provider's docs in November 2026;
 * update here when they change. Any other address or model id can still be
 * typed with Custom.
 */
export interface AIProvider {
  id: string
  label: string
  baseURL: string
  models: string[]
}

export const AI_PROVIDERS: AIProvider[] = [
  {
    // https://platform.claude.com/docs/en/api/openai-sdk
    id: 'anthropic',
    label: 'Anthropic (Claude)',
    baseURL: 'https://api.anthropic.com/v1/',
    models: [
      'claude-opus-5-5',
      'claude-sonnet-5-5',
      'claude-haiku-5-5',
      'claude-fable-5-1',
      'claude-opus-5',
      'claude-sonnet-5',
      'claude-fable-5',
      'claude-opus-4-8',
      'claude-opus-4-7',
      'claude-sonnet-4-6',
    ],
  },
  {
    // https://developers.openai.com/api/docs/models/all
    id: 'openai',
    label: 'OpenAI',
    baseURL: 'https://api.openai.com/v1',
    models: ['gpt-6.1-sol', 'gpt-6.1-terra', 'gpt-6-astra', 'gpt-6-sol', 'gpt-6-luna', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.4-mini'],
  },
  {
    // https://openrouter.ai/api/v1/models
    id: 'openrouter',
    label: 'OpenRouter',
    baseURL: 'https://openrouter.ai/api/v1',
    models: [
      'anthropic/claude-opus-5.5',
      'anthropic/claude-sonnet-5.5',
      'anthropic/claude-haiku-5.5',
      'openai/gpt-6.1-sol',
      'openai/gpt-6.1-terra',
      'google/gemini-4-argon',
      'google/gemini-3.8-flash',
      'x-ai/grok-4.7',
      'deepseek/deepseek-v4.1-flash',
      'moonshotai/kimi-k3.1-pro',
    ],
  },
  {
    // https://aistudio.google.com/docs/openai, https://aistudio.google.com/docs/models
    id: 'gemini',
    label: 'Google Gemini',
    baseURL: 'https://generativelanguage.googleapis.com/v1beta/openai/',
    models: ['gemini-4-argon', 'gemini-3.8-flash', 'gemini-3.6-flash', 'gemini-3.5-flash-lite', 'gemini-3.1-flash-lite', 'gemini-3.1-pro-preview', 'gemini-3-flash-preview', 'gemini-2.5-flash-lite'],
  },
  {
    // https://docs.mistral.ai/api
    id: 'mistral',
    label: 'Mistral',
    baseURL: 'https://api.mistral.ai/v1',
    models: ['mistral-large-latest', 'mistral-small-latest'],
  },
  {
    // https://console.groq.com/docs/openai, https://console.groq.com/docs/models
    id: 'groq',
    label: 'Groq',
    baseURL: 'https://api.groq.com/openai/v1',
    models: ['openai/gpt-oss-120b', 'openai/gpt-oss-20b', 'llama-3.3-70b-versatile', 'llama-3.1-8b-instant', 'qwen/qwen3.8-27b', 'minimaxai/minimax-m2.7', 'meta-models/muse-spark-open'],
  },
  {
    // https://docs.together.ai/docs/openai-api-compatibility, https://docs.together.ai/docs/serverless-models
    id: 'together',
    label: 'Together AI',
    baseURL: 'https://api.together.ai/v1',
    models: [
      'deepseek-ai/DeepSeek-V4-Pro-0813',
      'deepseek-ai/DeepSeek-V4.1-Flash',
      'moonshotai/Kimi-K3.1-Pro',
      'moonshotai/Kimi-K3',
      'Qwen/Qwen3.7-Max',
      'Qwen/Qwen3.8-Flash',
      'MiniMaxAI/MiniMax-M3',
      'openai/gpt-oss-120b',
      'meta-models/Muse-Spark-Open-816B-A42B',
    ],
  },
  {
    // https://api-docs.deepseek.com/quick_start/pricing
    id: 'deepseek',
    label: 'DeepSeek',
    baseURL: 'https://api.deepseek.com',
    models: ['deepseek-flash', 'deepseek-v4-pro'],
  },
  {
    // https://docs.x.ai/docs/overview, https://docs.x.ai/docs/models
    id: 'xai',
    label: 'xAI (Grok)',
    baseURL: 'https://api.x.ai/v1',
    models: ['grok-4.7', 'grok-4.6', 'grok-4.5', 'grok-4.3', 'grok-build-0.1', 'grok-4.20-0309-reasoning', 'grok-4.20-0309-non-reasoning'],
  },
]

const bare = (u: string) => u.trim().replace(/\/+$/, '').toLowerCase()

/** The provider whose address baseURL is, ignoring a trailing slash. */
export function providerAt(baseURL: string): AIProvider | undefined {
  return AI_PROVIDERS.find((p) => bare(p.baseURL) === bare(baseURL))
}

/**
 * The OpenAI-compatible address of the settings in force: an empty base URL
 * is the provider's own API (from the platform config).
 */
export function addressOf(provider: string | undefined, baseURL: string | undefined): string {
  if (baseURL) return baseURL
  return AI_PROVIDERS.find((p) => p.id === (provider === 'anthropic' ? 'anthropic' : 'openai'))?.baseURL ?? ''
}
