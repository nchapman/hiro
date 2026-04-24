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
  /**
   * Toggle replay mode. While true, user-input emissions from xterm (including
   * xterm's auto-replies to DA1 / OSC color queries embedded in the replayed
   * PTY stream) are dropped so they are not echoed back into the live PTY.
   */
  setReplaying(replaying: boolean): void
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
    // True while the server is streaming the replay buffer on (re)attach.
    // Defense-in-depth with the server-side query filter: even if a new query
    // sequence slips through, xterm's auto-reply is dropped here.
    const replayingRef = useRef(false)
    // Incremented each time replay starts. Used to ignore stale write-drain
    // callbacks and timeout firings from an earlier replay cycle, which could
    // otherwise clear the flag while a newer replay is still streaming.
    const replayGenRef = useRef(0)
    // Fail-safe: force-clear the replay flag if replay_end never arrives.
    const replayTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)

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
      setReplaying(replaying: boolean) {
        if (replaying) {
          replayGenRef.current++
          replayingRef.current = true
          if (replayTimeoutRef.current) clearTimeout(replayTimeoutRef.current)
          const gen = replayGenRef.current
          // If replay_end never arrives (server crash, dropped frame), don't
          // silently eat the user's keystrokes forever.
          replayTimeoutRef.current = setTimeout(() => {
            if (replayGenRef.current === gen) replayingRef.current = false
          }, 5000)
          return
        }
        if (replayTimeoutRef.current) {
          clearTimeout(replayTimeoutRef.current)
          replayTimeoutRef.current = null
        }
        const gen = replayGenRef.current
        const term = termRef.current
        if (!term) {
          replayingRef.current = false
          return
        }
        // xterm parses writes asynchronously and can emit onData for queries
        // embedded in replay bytes after earlier writes drain. A zero-length
        // write's callback fires only after all prior writes are processed,
        // so any onData from the replay stream is still suppressed.
        term.write("", () => {
          if (replayGenRef.current === gen) replayingRef.current = false
        })
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

      termRef.current = term
      fitRef.current = fitAddon

      const dataDisposable = term.onData((data) => {
        if (replayingRef.current) return
        onData(sessionId, new TextEncoder().encode(data))
      })

      // Attach the resize listener BEFORE the initial fit — fit() fires
      // term.onResize synchronously, and the server's PTY would otherwise
      // remain stuck at the default size sent with the `create` control
      // message until the user manually resized the window.
      const resizeDisposable = term.onResize(({ cols, rows }) => {
        // Guard against a zero-dim fit (e.g. the container isn't laid out
        // yet). The visibility effect's rAF-delayed fit will pick up the
        // real size once layout settles.
        if (cols > 0 && rows > 0) onResize(sessionId, cols, rows)
      })

      fitAddon.fit()

      // Flush any writes that arrived before xterm was ready.
      for (const chunk of pendingRef.current) {
        term.write(chunk)
      }
      pendingRef.current = []

      const observer = new ResizeObserver(() => {
        if (visible) fitAddon.fit()
      })
      observer.observe(el)

      return () => {
        observer.disconnect()
        dataDisposable.dispose()
        resizeDisposable.dispose()
        if (replayTimeoutRef.current) {
          clearTimeout(replayTimeoutRef.current)
          replayTimeoutRef.current = null
        }
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
