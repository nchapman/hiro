import type { ThemeMeta, ThemeId, VSCodeTheme } from "./types"

export const BUNDLED_THEMES: ThemeMeta[] = [
  // Dark
  { id: "one-dark-pro", name: "One Dark Pro", type: "dark" },
  { id: "dracula", name: "Dracula", type: "dark" },
  { id: "github-dark", name: "GitHub Dark", type: "dark" },
  { id: "tokyo-night", name: "Tokyo Night", type: "dark" },
  { id: "catppuccin-mocha", name: "Catppuccin Mocha", type: "dark" },
  { id: "nord", name: "Nord", type: "dark" },
  { id: "solarized-dark", name: "Solarized Dark", type: "dark" },
  { id: "rose-pine", name: "Rosé Pine", type: "dark" },
  { id: "vitesse-dark", name: "Vitesse Dark", type: "dark" },
  // Light
  { id: "github-light", name: "GitHub Light", type: "light" },
  { id: "one-light", name: "One Light", type: "light" },
  { id: "solarized-light", name: "Solarized Light", type: "light" },
  { id: "catppuccin-latte", name: "Catppuccin Latte", type: "light" },
  { id: "rose-pine-dawn", name: "Rosé Pine Dawn", type: "light" },
]

export const DEFAULT_THEME_ID: ThemeId = "one-dark-pro"

const cache = new Map<ThemeId, VSCodeTheme>()

/** Dynamic import map — Vite code-splits each theme into its own chunk. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const loaders: Record<string, () => Promise<any>> = {
  "one-dark-pro": () => import("shiki/dist/themes/one-dark-pro.mjs"),
  "dracula": () => import("shiki/dist/themes/dracula.mjs"),
  "github-dark": () => import("shiki/dist/themes/github-dark.mjs"),
  "tokyo-night": () => import("shiki/dist/themes/tokyo-night.mjs"),
  "catppuccin-mocha": () => import("shiki/dist/themes/catppuccin-mocha.mjs"),
  "nord": () => import("shiki/dist/themes/nord.mjs"),
  "solarized-dark": () => import("shiki/dist/themes/solarized-dark.mjs"),
  "rose-pine": () => import("shiki/dist/themes/rose-pine.mjs"),
  "vitesse-dark": () => import("shiki/dist/themes/vitesse-dark.mjs"),
  "github-light": () => import("shiki/dist/themes/github-light.mjs"),
  "one-light": () => import("shiki/dist/themes/one-light.mjs"),
  "solarized-light": () => import("shiki/dist/themes/solarized-light.mjs"),
  "catppuccin-latte": () => import("shiki/dist/themes/catppuccin-latte.mjs"),
  "rose-pine-dawn": () => import("shiki/dist/themes/rose-pine-dawn.mjs"),
}

export async function loadTheme(id: ThemeId): Promise<VSCodeTheme> {
  const cached = cache.get(id)
  if (cached) return cached

  const loader = loaders[id]
  if (!loader) throw new Error(`Unknown theme: ${id}`)

  const mod = await loader()
  const theme = mod.default as VSCodeTheme
  cache.set(id, theme)
  return theme
}
