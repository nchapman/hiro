import { useEffect, useRef, useState } from "react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import { WebLinksAddon } from "@xterm/addon-web-links"
import "@xterm/xterm/css/xterm.css"

export default function TerminalPage() {
  const containerRef = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState<"connecting" | "connected" | "disconnected">("connecting")

  useEffect(() => {
    const el = containerRef.current
    if (!el) return

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: "'Geist Mono Variable', 'Geist Mono', Menlo, monospace",
      fontSize: 14,
      theme: {
        background: "#282c34",
        foreground: "#abb2bf",
        cursor: "#528bff",
        selectionBackground: "#3e4451",
        black: "#282c34",
        red: "#e06c75",
        green: "#98c379",
        yellow: "#e5c07b",
        blue: "#61afef",
        magenta: "#c678dd",
        cyan: "#56b6c2",
        white: "#abb2bf",
        brightBlack: "#5c6370",
        brightRed: "#e06c75",
        brightGreen: "#98c379",
        brightYellow: "#e5c07b",
        brightBlue: "#61afef",
        brightMagenta: "#c678dd",
        brightCyan: "#56b6c2",
        brightWhite: "#ffffff",
      },
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(new WebLinksAddon())
    term.open(el)
    fitAddon.fit()

    // Connect WebSocket.
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
    const params = new URLSearchParams({ cols: String(term.cols), rows: String(term.rows) })
    // Forward dir param from page URL to WebSocket if present.
    const pageParams = new URLSearchParams(window.location.search)
    const dir = pageParams.get("dir")
    if (dir) params.set("dir", dir)
    const ws = new WebSocket(`${proto}//${window.location.host}/ws/terminal?${params}`)
    ws.binaryType = "arraybuffer"

    ws.onopen = () => {
      setStatus("connected")
    }

    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(ev.data))
      } else {
        // Text frame: JSON control message.
        try {
          const msg = JSON.parse(ev.data as string)
          if (msg.type === "exited") {
            term.write(`\r\n\x1b[90m[shell exited with code ${msg.code ?? "?"}]\x1b[0m\r\n`)
            setStatus("disconnected")
          } else if (msg.type === "error") {
            term.write(`\r\n\x1b[31m[error: ${msg.message}]\x1b[0m\r\n`)
          }
        } catch {
          // Ignore malformed text frames.
        }
      }
    }

    ws.onclose = () => {
      setStatus("disconnected")
      term.write("\r\n\x1b[90m[connection closed]\x1b[0m\r\n")
    }

    // Terminal input → WebSocket.
    const dataDisposable = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(new TextEncoder().encode(data))
      }
    })

    // Terminal resize → WebSocket + PTY.
    const resizeDisposable = term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols, rows }))
      }
    })

    // Fit terminal when container resizes.
    const observer = new ResizeObserver(() => {
      fitAddon.fit()
    })
    observer.observe(el)

    // Set page title.
    document.title = "Hive Terminal"

    return () => {
      observer.disconnect()
      dataDisposable.dispose()
      resizeDisposable.dispose()
      ws.close()
      term.dispose()
    }
  }, [])

  return (
    <div className="h-screen w-screen bg-[#282c34] flex flex-col">
      {status === "connecting" && (
        <div className="absolute inset-0 flex items-center justify-center text-[#abb2bf]/50 text-sm z-10">
          Connecting...
        </div>
      )}
      <div ref={containerRef} className="flex-1 min-h-0 p-1" />
    </div>
  )
}
