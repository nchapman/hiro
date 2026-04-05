/**
 * Maps VS Code theme `colors` to shadcn CSS custom properties.
 *
 * Fallback chains follow VS Code's own color derivation logic documented in
 * colorRegistry.ts and the Theme Color API reference. Key principles:
 *
 * - Control surfaces (inputs, dropdowns) use `input.background` / `dropdown.background`
 *   which are lighter than `editor.background` on dark themes (by convention)
 * - Control borders use `input.border` / `dropdown.border` / `editorWidget.border`,
 *   NOT `editorGroup.border` (which is for subtle pane dividers)
 * - Secondary buttons/badges use `button.secondaryBackground` / `badge.background`
 *
 * See: https://code.visualstudio.com/api/references/theme-color
 */
export function mapCssVars(colors: Record<string, string>): Record<string, string> {
  const c = (key: string, ...fallbacks: string[]): string => {
    const val = normalizeColor(colors[key])
    if (val) return val
    for (const fb of fallbacks) {
      const fbVal = normalizeColor(colors[fb])
      if (fbVal) return fbVal
    }
    return ""
  }

  // --- Extract VS Code color keys ---

  const bg = c("editor.background")
  const fg = c("editor.foreground")

  // Surfaces
  const sidebarBg = c("sideBar.background", "editorGroupHeader.tabsBackground")
  const widgetBg = c("editorWidget.background", "editorHoverWidget.background")
  const tabInactiveBg = c("tab.inactiveBackground", "editorGroupHeader.tabsBackground")

  // Control colors (inputs, dropdowns, buttons) — the interactive UI surface
  const controlBg = c("input.background", "dropdown.background")
  const controlBorder = c("input.border", "dropdown.border", "editorWidget.border")
  const secondaryBg = c("button.secondaryBackground", "badge.background", "input.background")
  const secondaryFg = c("button.secondaryForeground")

  // Action colors
  const buttonBg = c("button.background")
  const buttonFg = c("button.foreground")
  const focus = c("focusBorder")

  // Text hierarchy
  const descFg = c("descriptionForeground", "tab.inactiveForeground", "sideBar.foreground")
  const sidebarFg = c("sideBar.foreground")

  // Lists
  const listHover = c("list.hoverBackground")
  const listActiveBg = c("list.activeSelectionBackground")
  const listActiveFg = c("list.activeSelectionForeground")
  const lineHighlight = c("editor.lineHighlightBackground")

  // Structural borders (pane dividers — subtle)
  const paneBorder = c("editorGroup.border", "panel.border")

  // Errors
  const errorFg = c("editorError.foreground", "errorForeground")

  // Sidebar section headers
  const sidebarHeaderFg = c("sideBarSectionHeader.foreground")

  // --- Build CSS var mapping ---

  const vars: Record<string, string> = {}
  const set = (name: string, value: string) => {
    if (value) vars[name] = value
  }

  // Core surfaces
  set("--background", bg)
  set("--foreground", fg)

  // Cards / popovers — widget surface
  set("--card", widgetBg || sidebarBg || adjustBrightness(bg, 0.05))
  set("--card-foreground", c("editorWidget.foreground") || fg)
  set("--popover", widgetBg || adjustBrightness(bg, 0.05))
  set("--popover-foreground", c("editorWidget.foreground") || fg)

  // Primary action — button.background (the accent/CTA color)
  set("--primary", buttonBg || focus)
  set("--primary-foreground", buttonFg || "#ffffff")

  // Secondary — button.secondaryBackground / badge.background
  // Used for outline badges, secondary buttons, and other interactive chrome.
  // Pre-composite against bg to avoid stacking artifacts from semi-transparent values.
  set("--secondary", composite(secondaryBg, bg) || adjustBrightness(bg, 0.1))
  set("--secondary-foreground", secondaryFg || fg)

  // Muted — inactive/subtle areas
  set("--muted", tabInactiveBg || adjustBrightness(bg, 0.03))
  set("--muted-foreground", descFg || adjustAlpha(fg, 0.55))

  // Accent — hover/active highlight
  set("--accent", listHover || lineHighlight || adjustBrightness(bg, 0.06))
  set("--accent-foreground", c("list.hoverForeground") || fg)

  // Destructive
  set("--destructive", errorFg || "#e06c75")
  set("--destructive-foreground", "#ffffff")

  // Borders — control borders (input.border / dropdown.border) for interactive
  // elements, NOT editorGroup.border which is for subtle pane dividers
  set("--border", controlBorder || paneBorder || adjustAlpha(fg, 0.15))

  // Input surface — the interactive control background
  set("--input", controlBg || adjustBrightness(bg, 0.08))

  // Focus ring
  set("--ring", focus || buttonBg)

  // Sidebar
  const sidebarBorderVal = c("sideBar.border") || paneBorder || adjustAlpha(fg, 0.1)
  set("--sidebar", sidebarBg || adjustBrightness(bg, -0.03))
  set("--sidebar-foreground", sidebarFg || fg)
  set("--sidebar-primary", sidebarHeaderFg || buttonBg)
  set("--sidebar-primary-foreground", sidebarFg || fg)
  set("--sidebar-accent", listActiveBg || listHover || adjustBrightness(bg, 0.08))
  set("--sidebar-accent-foreground", listActiveFg || fg)
  set("--sidebar-border", sidebarBorderVal)
  set("--sidebar-ring", focus || buttonBg)

  // Charts — derived from the link/accent color
  const chartBase = c("textLink.foreground", "focusBorder", "button.background")
  if (chartBase) {
    set("--chart-1", adjustBrightness(chartBase, 0.2))
    set("--chart-2", chartBase)
    set("--chart-3", adjustBrightness(chartBase, -0.1))
    set("--chart-4", adjustBrightness(chartBase, -0.2))
    set("--chart-5", adjustBrightness(chartBase, -0.3))
  }

  return vars
}

// --- Color helpers ---

/**
 * Normalize a hex color value. Handles 3, 6, and 8-char hex.
 * Returns null for fully transparent values or non-hex input.
 * Converts 8-char hex (with alpha) to rgba().
 */
function normalizeColor(hex: string | undefined): string | null {
  if (!hex) return null
  const m = hex.match(/^#([0-9a-f]{3,8})$/i)
  if (!m) return null

  let h = m[1]
  if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2]

  if (h.length === 8) {
    const alpha = parseInt(h.slice(6, 8), 16) / 255
    if (alpha === 0) return null
    const r = parseInt(h.slice(0, 2), 16)
    const g = parseInt(h.slice(2, 4), 16)
    const b = parseInt(h.slice(4, 6), 16)
    if (alpha >= 0.99) return toHex(r, g, b)
    return `rgba(${r}, ${g}, ${b}, ${alpha.toFixed(3)})`
  }

  if (h.length === 6) return `#${h}`
  return null
}

function parseHex(hex: string): [number, number, number] | null {
  const rgbaMatch = hex.match(/^rgba?\((\d+),\s*(\d+),\s*(\d+)/)
  if (rgbaMatch) {
    return [parseInt(rgbaMatch[1]), parseInt(rgbaMatch[2]), parseInt(rgbaMatch[3])]
  }
  const m = hex.match(/^#?([0-9a-f]{6})$/i)
  if (!m) return null
  return [
    parseInt(m[1].slice(0, 2), 16),
    parseInt(m[1].slice(2, 4), 16),
    parseInt(m[1].slice(4, 6), 16),
  ]
}

function toHex(r: number, g: number, b: number): string {
  const clamp = (v: number) => Math.max(0, Math.min(255, Math.round(v)))
  return `#${[r, g, b].map((v) => clamp(v).toString(16).padStart(2, "0")).join("")}`
}

/** Adjust perceived brightness via HSL lightness (works correctly on both light and dark colors). */
function adjustBrightness(hex: string, amount: number): string {
  const rgb = parseHex(hex)
  if (!rgb) return hex
  const [h, s, l] = rgbToHsl(...rgb)
  const newL = Math.max(0, Math.min(1, l + amount))
  const [r, g, b] = hslToRgb(h, s, newL)
  return toHex(r, g, b)
}

function rgbToHsl(r: number, g: number, b: number): [number, number, number] {
  r /= 255; g /= 255; b /= 255
  const max = Math.max(r, g, b), min = Math.min(r, g, b)
  const l = (max + min) / 2
  if (max === min) return [0, 0, l]
  const d = max - min
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min)
  let h = 0
  if (max === r) h = ((g - b) / d + (g < b ? 6 : 0)) / 6
  else if (max === g) h = ((b - r) / d + 2) / 6
  else h = ((r - g) / d + 4) / 6
  return [h, s, l]
}

function hslToRgb(h: number, s: number, l: number): [number, number, number] {
  if (s === 0) { const v = l * 255; return [v, v, v] }
  const hue2rgb = (p: number, q: number, t: number) => {
    if (t < 0) t += 1; if (t > 1) t -= 1
    if (t < 1/6) return p + (q - p) * 6 * t
    if (t < 1/2) return q
    if (t < 2/3) return p + (q - p) * (2/3 - t) * 6
    return p
  }
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s
  const p = 2 * l - q
  return [hue2rgb(p, q, h + 1/3) * 255, hue2rgb(p, q, h) * 255, hue2rgb(p, q, h - 1/3) * 255]
}

/**
 * Pre-composite a potentially semi-transparent color against a background.
 * If the color is already opaque or empty, returns it unchanged.
 * This prevents stacking artifacts when rgba values are used as CSS var backgrounds.
 */
function composite(color: string, bg: string): string {
  if (!color) return ""
  const rgbaMatch = color.match(/^rgba?\((\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\)/)
  if (!rgbaMatch) return color // already opaque hex
  const [, rs, gs, bs, as] = rgbaMatch
  const alpha = parseFloat(as)
  if (alpha >= 0.99) return toHex(parseInt(rs), parseInt(gs), parseInt(bs))
  const bgRgb = parseHex(bg)
  if (!bgRgb) return color
  const [br, bg2, bb] = bgRgb
  return toHex(
    parseInt(rs) * alpha + br * (1 - alpha),
    parseInt(gs) * alpha + bg2 * (1 - alpha),
    parseInt(bs) * alpha + bb * (1 - alpha),
  )
}

function adjustAlpha(hex: string, alpha: number): string {
  const rgb = parseHex(hex)
  if (!rgb) return hex
  const [r, g, b] = rgb
  const a = Math.max(0, Math.min(1, alpha))
  return `rgba(${r}, ${g}, ${b}, ${a})`
}
