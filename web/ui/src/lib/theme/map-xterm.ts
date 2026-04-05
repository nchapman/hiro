import type { ITheme } from "@xterm/xterm"

/** Maps VS Code theme `colors` to an xterm.js ITheme. */
export function buildXtermTheme(colors: Record<string, string>): ITheme {
  return {
    background: colors["terminal.background"] ?? colors["editor.background"] ?? "#1e1e1e",
    foreground: colors["terminal.foreground"] ?? colors["editor.foreground"] ?? "#cccccc",
    cursor: colors["terminalCursor.foreground"] || colors["editorCursor.foreground"],
    cursorAccent: colors["terminalCursor.background"],
    selectionBackground: colors["terminal.selectionBackground"] || colors["editor.selectionBackground"],
    selectionForeground: colors["terminal.selectionForeground"],
    black: colors["terminal.ansiBlack"],
    red: colors["terminal.ansiRed"],
    green: colors["terminal.ansiGreen"],
    yellow: colors["terminal.ansiYellow"],
    blue: colors["terminal.ansiBlue"],
    magenta: colors["terminal.ansiMagenta"],
    cyan: colors["terminal.ansiCyan"],
    white: colors["terminal.ansiWhite"],
    brightBlack: colors["terminal.ansiBrightBlack"],
    brightRed: colors["terminal.ansiBrightRed"],
    brightGreen: colors["terminal.ansiBrightGreen"],
    brightYellow: colors["terminal.ansiBrightYellow"],
    brightBlue: colors["terminal.ansiBrightBlue"],
    brightMagenta: colors["terminal.ansiBrightMagenta"],
    brightCyan: colors["terminal.ansiBrightCyan"],
    brightWhite: colors["terminal.ansiBrightWhite"],
  }
}
