import type { Extension } from "@codemirror/state"
import type { ITheme } from "@xterm/xterm"

export type ThemeId = string

export interface ThemeMeta {
  id: ThemeId
  name: string
  type: "dark" | "light"
}

export interface ResolvedTheme {
  cssVars: Record<string, string>
  codemirrorExtension: Extension
  xtermTheme: ITheme
  shikiThemeName: string
  isDark: boolean
}

/** Subset of a VS Code / Shiki theme object that we consume. */
export interface VSCodeTheme {
  name?: string
  type?: string
  colors?: Record<string, string>
  tokenColors?: TokenColor[]
}

export interface TokenColor {
  name?: string
  scope?: string | string[]
  settings: {
    foreground?: string
    background?: string
    fontStyle?: string
  }
}
