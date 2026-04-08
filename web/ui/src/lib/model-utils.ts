/** Split "provider/model" into [provider, model]. Bare model -> ["", model]. */
export function parseModelSpec(spec: string): [string, string] {
  const i = spec.indexOf("/")
  if (i < 0) return ["", spec]
  return [spec.slice(0, i), spec.slice(i + 1)]
}

/** Join provider + model into "provider/model". Bare model -> just model. */
export function formatModelSpec(provider: string, model: string): string {
  if (!provider) return model
  return `${provider}/${model}`
}
