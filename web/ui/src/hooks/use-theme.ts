import { useState, useEffect, useCallback, createContext, useContext, useRef } from "react"
import type { ThemeId, ThemeMeta, ResolvedTheme } from "@/lib/theme"
import { BUNDLED_THEMES, DEFAULT_THEME_ID, loadTheme, resolveTheme } from "@/lib/theme"

const STORAGE_KEY = "hiro-theme-id"
const OLD_STORAGE_KEY = "hiro-theme"

interface ThemeContext {
  themeId: ThemeId
  resolved: ResolvedTheme | null
  setThemeId: (id: ThemeId) => void
  availableThemes: ThemeMeta[]
}

const ThemeCtx = createContext<ThemeContext>({
  themeId: DEFAULT_THEME_ID,
  resolved: null,
  setThemeId: () => {},
  availableThemes: BUNDLED_THEMES,
})

export function useTheme() {
  return useContext(ThemeCtx)
}

export { ThemeCtx }

/** Migrate from old light/dark/system storage to a theme ID. */
function migrateOldTheme(): ThemeId | null {
  const old = localStorage.getItem(OLD_STORAGE_KEY)
  if (!old) return null
  localStorage.removeItem(OLD_STORAGE_KEY)
  if (old === "light") return "github-light"
  if (old === "dark") return DEFAULT_THEME_ID
  // "system" — pick based on current preference
  if (window.matchMedia("(prefers-color-scheme: light)").matches) return "github-light"
  return DEFAULT_THEME_ID
}

function getStoredThemeId(): ThemeId {
  const migrated = migrateOldTheme()
  if (migrated) {
    localStorage.setItem(STORAGE_KEY, migrated)
    return migrated
  }
  return localStorage.getItem(STORAGE_KEY) || DEFAULT_THEME_ID
}

/** Track which CSS vars were set so we can clean up stale ones on theme switch. */
let previousVarNames = new Set<string>()

/** Apply CSS custom properties from the resolved theme to the document root. */
function applyCssVars(vars: Record<string, string>) {
  const style = document.documentElement.style
  const nextNames = new Set<string>()

  for (const [key, value] of Object.entries(vars)) {
    if (value) {
      style.setProperty(key, value)
      nextNames.add(key)
    }
  }

  // Remove vars that the new theme doesn't provide
  for (const name of previousVarNames) {
    if (!nextNames.has(name)) {
      style.removeProperty(name)
    }
  }

  previousVarNames = nextNames
}

export function useThemeProvider() {
  const [themeId, setThemeIdState] = useState<ThemeId>(getStoredThemeId)
  const [resolved, setResolved] = useState<ResolvedTheme | null>(null)
  const loadingRef = useRef<ThemeId | null>(null)

  const applyTheme = useCallback(async (id: ThemeId) => {
    loadingRef.current = id
    try {
      const themeObj = await loadTheme(id)
      // Guard against stale loads (user switched again before this resolved)
      if (loadingRef.current !== id) return
      const result = resolveTheme(themeObj, id)
      applyCssVars(result.cssVars)
      document.documentElement.classList.toggle("dark", result.isDark)
      setResolved(result)
    } catch (err) {
      console.error(`Failed to load theme "${id}":`, err)
      // Fall back to default if the requested theme fails and we haven't been superseded
      if (id !== DEFAULT_THEME_ID && loadingRef.current === id) {
        loadingRef.current = DEFAULT_THEME_ID
        try {
          const fallback = await loadTheme(DEFAULT_THEME_ID)
          if (loadingRef.current !== DEFAULT_THEME_ID) return
          const result = resolveTheme(fallback, DEFAULT_THEME_ID)
          applyCssVars(result.cssVars)
          document.documentElement.classList.toggle("dark", result.isDark)
          setResolved(result)
        } catch {
          // Both failed — nothing we can do, CSS fallback in index.css handles it
        }
      }
    }
  }, [])

  const setThemeId = useCallback((id: ThemeId) => {
    localStorage.setItem(STORAGE_KEY, id)
    setThemeIdState(id)
    applyTheme(id)
  }, [applyTheme])

  // Load initial theme on mount
  useEffect(() => {
    applyTheme(themeId)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  return { themeId, resolved, setThemeId, availableThemes: BUNDLED_THEMES }
}
