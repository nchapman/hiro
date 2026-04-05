import { EditorView } from "@codemirror/view"
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language"
import { tags } from "@lezer/highlight"
import type { Extension } from "@codemirror/state"
import type { Tag } from "@lezer/highlight"
import type { VSCodeTheme, TokenColor } from "./types"

/**
 * Maps common TextMate scope prefixes to CodeMirror/lezer highlight tags.
 * Ordered from most specific to least specific — first match wins.
 */
const SCOPE_TO_TAGS: [string, Tag[]][] = [
  ["comment.block.documentation", [tags.docComment]],
  ["comment", [tags.comment]],
  ["string.regexp", [tags.regexp]],
  ["string", [tags.string]],
  ["constant.numeric", [tags.number]],
  ["constant.language.boolean", [tags.bool]],
  ["constant.language.null", [tags.null]],
  ["constant.language.undefined", [tags.null]],
  ["constant.language", [tags.atom]],
  ["constant.character.escape", [tags.escape]],
  ["constant.other", [tags.constant(tags.variableName)]],
  ["variable.parameter", [tags.variableName]],
  ["variable.language", [tags.special(tags.variableName)]],
  ["variable.other.constant", [tags.constant(tags.variableName)]],
  ["variable", [tags.variableName]],
  ["keyword.operator.assignment", [tags.definitionOperator]],
  ["keyword.operator.comparison", [tags.compareOperator]],
  ["keyword.operator.logical", [tags.logicOperator]],
  ["keyword.operator.arithmetic", [tags.arithmeticOperator]],
  ["keyword.operator", [tags.operator]],
  ["keyword.control.import", [tags.keyword]],
  ["keyword.control.flow", [tags.controlKeyword]],
  ["keyword", [tags.keyword]],
  ["storage.type.function", [tags.keyword]],
  ["storage.type", [tags.keyword]],
  ["storage.modifier", [tags.modifier]],
  ["entity.name.function.method", [tags.function(tags.propertyName)]],
  ["entity.name.function", [tags.function(tags.variableName)]],
  ["entity.name.type.class", [tags.className]],
  ["entity.name.type.interface", [tags.typeName]],
  ["entity.name.type", [tags.typeName]],
  ["entity.name.tag", [tags.tagName]],
  ["entity.name.section", [tags.heading]],
  ["entity.name.namespace", [tags.namespace]],
  ["entity.other.attribute-name", [tags.attributeName]],
  ["entity.other.inherited-class", [tags.className]],
  ["support.function", [tags.function(tags.variableName)]],
  ["support.type", [tags.typeName]],
  ["support.class", [tags.className]],
  ["support.constant", [tags.constant(tags.variableName)]],
  ["support.type.property-name", [tags.propertyName]],
  ["meta.decorator", [tags.meta]],
  ["meta.annotation", [tags.meta]],
  ["punctuation.definition.tag", [tags.angleBracket]],
  ["punctuation.separator", [tags.separator]],
  ["punctuation", [tags.punctuation]],
  ["markup.heading", [tags.heading]],
  ["markup.bold", [tags.strong]],
  ["markup.italic", [tags.emphasis]],
  ["markup.deleted", [tags.content]],   // Use content tag for deleted
  ["markup.inserted", [tags.content]],  // Use content tag for inserted
  ["markup.inline.raw", [tags.monospace]],
  ["markup.underline.link", [tags.link]],
  ["invalid.illegal", [tags.invalid]],
  ["invalid", [tags.invalid]],
]

/** Find the best-matching lezer tags for a TextMate scope string. */
function scopeToTags(scope: string): Tag[] {
  for (const [prefix, cmTags] of SCOPE_TO_TAGS) {
    if (scope === prefix || scope.startsWith(prefix + ".")) {
      return cmTags
    }
  }
  return []
}

/** Build a complete CodeMirror theme Extension from a VS Code theme. */
export function buildCodemirrorTheme(theme: VSCodeTheme): Extension {
  const colors = theme.colors || {}
  const tokenColors = theme.tokenColors || []
  const isDark = theme.type !== "light"

  // Editor chrome
  const editorTheme = EditorView.theme({
    "&": {
      color: colors["editor.foreground"] || "",
      backgroundColor: colors["editor.background"] || "",
    },
    ".cm-content": {
      caretColor: colors["editorCursor.foreground"] || "",
    },
    ".cm-cursor, .cm-dropCursor": {
      borderLeftColor: colors["editorCursor.foreground"] || "",
    },
    "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": {
      backgroundColor: colors["editor.selectionBackground"] || "",
    },
    ".cm-gutters": {
      backgroundColor: colors["editorGutter.background"] || colors["editor.background"] || "",
      color: colors["editorLineNumber.foreground"] || "",
      border: "none",
    },
    ".cm-activeLineGutter": {
      backgroundColor: colors["editor.lineHighlightBackground"] || "",
      color: colors["editorLineNumber.activeForeground"] || "",
    },
    ".cm-activeLine": {
      backgroundColor: colors["editor.lineHighlightBackground"] || "",
      // Some themes (e.g. Dracula) use a border instead of a background for the active line
      ...(colors["editor.lineHighlightBorder"] && !colors["editor.lineHighlightBackground"]
        ? { outline: `1px solid ${colors["editor.lineHighlightBorder"]}` }
        : {}),
    },
    ".cm-matchingBracket": {
      backgroundColor: colors["editorBracketMatch.background"] || "",
      outline: colors["editorBracketMatch.border"]
        ? `1px solid ${colors["editorBracketMatch.border"]}`
        : "none",
    },
    ".cm-searchMatch": {
      backgroundColor: colors["editor.findMatchHighlightBackground"] || "",
    },
    ".cm-searchMatch.cm-searchMatch-selected": {
      backgroundColor: colors["editor.findMatchBackground"] || "",
    },
    ".cm-tooltip": {
      backgroundColor: colors["editorHoverWidget.background"] || colors["editorWidget.background"] || "",
      border: `1px solid ${colors["editorHoverWidget.border"] || colors["editorWidget.border"] || "transparent"}`,
    },
    ".cm-tooltip-autocomplete": {
      backgroundColor: colors["editorSuggestWidget.background"] || "",
      border: `1px solid ${colors["editorSuggestWidget.border"] || "transparent"}`,
    },
    ".cm-tooltip-autocomplete .cm-completionMatchedText": {
      textDecoration: "none",
      color: colors["list.highlightForeground"] || "",
    },
    ".cm-foldPlaceholder": {
      backgroundColor: colors["editor.foldBackground"] || "transparent",
      border: "none",
    },
  }, { dark: isDark })

  // Syntax highlighting
  const tagStyles: { tag: Tag | Tag[]; color?: string; fontStyle?: string; fontWeight?: string; textDecoration?: string }[] = []
  const seen = new Set<string>()

  for (const rule of tokenColors) {
    if (!rule.scope || !rule.settings.foreground) continue
    const scopes = Array.isArray(rule.scope) ? rule.scope : [rule.scope]
    for (const scope of scopes) {
      const cmTags = scopeToTags(scope)
      if (cmTags.length === 0) continue
      // Deduplicate — first match per tag wins (more specific scopes come first in most themes)
      const key = cmTags.map((t) => t.toString()).join(",")
      if (seen.has(key)) continue
      seen.add(key)

      tagStyles.push({
        tag: cmTags,
        color: rule.settings.foreground,
        fontStyle: rule.settings.fontStyle?.includes("italic") ? "italic" : undefined,
        fontWeight: rule.settings.fontStyle?.includes("bold") ? "bold" : undefined,
        textDecoration: rule.settings.fontStyle?.includes("underline") ? "underline" : undefined,
      })
    }
  }

  // Add a default foreground style from the global scope if present
  const globalFg = findGlobalForeground(tokenColors)
  if (globalFg) {
    tagStyles.push({ tag: tags.content, color: globalFg })
  }

  const highlightStyle = HighlightStyle.define(tagStyles)

  return [editorTheme, syntaxHighlighting(highlightStyle)]
}

/** Find the foreground color from a global/default tokenColor rule. */
function findGlobalForeground(tokenColors: readonly TokenColor[]): string | undefined {
  for (const rule of tokenColors) {
    if (!rule.scope && rule.settings.foreground) return rule.settings.foreground
    if (rule.scope === "meta.embedded" && rule.settings.foreground) return rule.settings.foreground
  }
  return undefined
}
