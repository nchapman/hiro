import { useState, useEffect, useCallback, createContext, useContext } from "react"

type Theme = "light" | "dark" | "system"

interface ThemeContext {
  theme: Theme
  resolved: "light" | "dark"
  setTheme: (theme: Theme) => void
}

const ThemeCtx = createContext<ThemeContext>({
  theme: "system",
  resolved: "dark",
  setTheme: () => {},
})

export function useTheme() {
  return useContext(ThemeCtx)
}

export { ThemeCtx }

function getSystemTheme(): "light" | "dark" {
  return window.matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light"
}

function resolve(theme: Theme): "light" | "dark" {
  return theme === "system" ? getSystemTheme() : theme
}

export function useThemeProvider() {
  const [theme, setThemeState] = useState<Theme>(() => {
    const stored = localStorage.getItem("hive-theme")
    return (stored as Theme) ?? "system"
  })

  const [resolved, setResolved] = useState<"light" | "dark">(() =>
    resolve(theme)
  )

  const apply = useCallback((t: Theme) => {
    const r = resolve(t)
    setResolved(r)
    document.documentElement.classList.toggle("dark", r === "dark")
  }, [])

  const setTheme = useCallback(
    (t: Theme) => {
      localStorage.setItem("hive-theme", t)
      setThemeState(t)
      apply(t)
    },
    [apply]
  )

  // Apply on mount
  useEffect(() => {
    apply(theme)
  }, [apply, theme])

  // Listen for system changes when in "system" mode
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)")
    const handler = () => {
      if (theme === "system") apply("system")
    }
    mq.addEventListener("change", handler)
    return () => mq.removeEventListener("change", handler)
  }, [theme, apply])

  return { theme, resolved, setTheme }
}
