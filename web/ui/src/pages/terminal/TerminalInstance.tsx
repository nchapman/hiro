import { useEffect, useRef, useImperativeHandle, forwardRef } from "react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import { WebLinksAddon } from "@xterm/addon-web-links"
import type { ITheme } from "@xterm/xterm"
import "@xterm/xterm/css/xterm.css"

export interface TerminalInstanceHandle {
  /** Write raw PTY output to the terminal. */
  write(data: Uint8Array): void
  /** Write a string to the terminal (for status messages). */
  writeString(s: string): void
  /** Refit the terminal to its container. */
  fit(): void
}

interface TerminalInstanceProps {
  sessionId: string
  visible: boolean
  xtermTheme?: ITheme
  /** Called when the user types — parent sends this over the WebSocket. */
  onData: (sessionId: string, data: Uint8Array) => void
  /** Called when the terminal resizes — parent sends resize control. */
  onResize: (sessionId: string, cols: number, rows: number) => void
}

const TerminalInstance = forwardRef<TerminalInstanceHandle, TerminalInstanceProps>(
  function TerminalInstance({ sessionId, visible, xtermTheme, onData, onResize }, ref) {
    const containerRef = useRef<HTMLDivElement>(null)
    const termRef = useRef<Terminal | null>(null)
    const fitRef = useRef<FitAddon | null>(null)
    // Buffer writes that arrive before xterm is initialized.
    const pendingRef = useRef<Uint8Array[]>([])

    // Expose imperative methods to parent. Writes are buffered until xterm
    // is ready — the useEffect below flushes pendingRef on init.
    useImperativeHandle(ref, () => ({
      write(data: Uint8Array) {
        if (termRef.current) {
          termRef.current.write(data)
        } else {
          pendingRef.current.push(data)
        }
      },
      writeString(s: string) {
        if (termRef.current) {
          termRef.current.write(s)
        }
      },
      fit() {
        fitRef.current?.fit()
      },
    }))

    // Create the terminal once on mount.
    useEffect(() => {
      const el = containerRef.current
      if (!el) return

      const term = new Terminal({
        cursorBlink: true,
        fontFamily: "'Geist Mono Variable', 'Geist Mono', Menlo, monospace",
        fontSize: 14,
        theme: xtermTheme,
      })

      const fitAddon = new FitAddon()
      term.loadAddon(fitAddon)
      term.loadAddon(new WebLinksAddon())
      term.open(el)
      fitAddon.fit()

      termRef.current = term
      fitRef.current = fitAddon

      // Flush any writes that arrived before xterm was ready.
      for (const chunk of pendingRef.current) {
        term.write(chunk)
      }
      pendingRef.current = []

      const dataDisposable = term.onData((data) => {
        onData(sessionId, new TextEncoder().encode(data))
      })

      const resizeDisposable = term.onResize(({ cols, rows }) => {
        onResize(sessionId, cols, rows)
      })

      const observer = new ResizeObserver(() => {
        if (visible) fitAddon.fit()
      })
      observer.observe(el)

      return () => {
        observer.disconnect()
        dataDisposable.dispose()
        resizeDisposable.dispose()
        term.dispose()
        termRef.current = null
        fitRef.current = null
      }
      // sessionId is stable for the lifetime of this component.
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [sessionId])

    // Update theme at runtime when it changes.
    useEffect(() => {
      if (termRef.current && xtermTheme) {
        termRef.current.options.theme = xtermTheme
      }
    }, [xtermTheme])

    // Re-fit and focus when visibility changes.
    useEffect(() => {
      if (visible) {
        requestAnimationFrame(() => {
          fitRef.current?.fit()
          termRef.current?.focus()
        })
      }
    }, [visible])

    return (
      <div
        ref={containerRef}
        className="flex-1 min-h-0 p-1"
        style={{ display: visible ? "block" : "none" }}
      />
    )
  },
)

export default TerminalInstance
