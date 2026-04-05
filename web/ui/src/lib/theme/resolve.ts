import type { VSCodeTheme, ResolvedTheme } from "./types"
import { mapCssVars } from "./map-css-vars"
import { buildCodemirrorTheme } from "./map-codemirror"
import { buildXtermTheme } from "./map-xterm"

export function resolveTheme(theme: VSCodeTheme, id: string): ResolvedTheme {
  const colors = theme.colors || {}
  return {
    cssVars: mapCssVars(colors),
    codemirrorExtension: buildCodemirrorTheme(theme),
    xtermTheme: buildXtermTheme(colors),
    shikiThemeName: id,
    isDark: theme.type !== "light",
  }
}
