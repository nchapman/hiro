import { useEffect, useRef, useState, useCallback } from "react"
import TerminalTabBar, { type TerminalTab } from "./TerminalTabBar"
import TerminalInstance, { type TerminalInstanceHandle } from "./TerminalInstance"
import "@xterm/xterm/css/xterm.css"

// Wire protocol constants — must match server.
const MSG_OUTPUT = 0x01
const MSG_INPUT = 0x02
const MSG_CONTROL = 0x03
const SESSION_ID_LEN = 32

const encoder = new TextEncoder()
const decoder = new TextDecoder()

/** Build a binary frame: [type (1)] [sessionId (32)] [payload] */
function buildFrame(type: number, sessionId: string, payload: Uint8Array): ArrayBuffer {
  const idBytes = encoder.encode(sessionId.padEnd(SESSION_ID_LEN, "\0"))
  const frame = new Uint8Array(1 + SESSION_ID_LEN + payload.length)
  frame[0] = type
  frame.set(idBytes.slice(0, SESSION_ID_LEN), 1)
  frame.set(payload, 1 + SESSION_ID_LEN)
  return frame.buffer
}

/** Parse the fixed header from a binary frame. */
function parseFrame(data: ArrayBuffer): { type: number; sessionId: string; payload: Uint8Array } {
  const view = new Uint8Array(data)
  const type = view[0]
  const sessionId = decoder.decode(view.slice(1, 1 + SESSION_ID_LEN)).replace(/\0+$/, "")
  const payload = view.slice(1 + SESSION_ID_LEN)
  return { type, sessionId, payload }
}

interface ControlMessage {
  type: string
  session_id?: string
  node_id?: string
  node_name?: string
  cols?: number
  rows?: number
  code?: number
  message?: string
}

export default function TerminalPage() {
  const [tabs, setTabs] = useState<TerminalTab[]>([])
  const [activeTabId, setActiveTabId] = useState<string | null>(null)
  const [status, setStatus] = useState<"connecting" | "connected" | "disconnected">("connecting")
  const [error, setError] = useState<string | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const instanceRefs = useRef<Map<string, TerminalInstanceHandle>>(new Map())

  const setInstanceRef = useCallback((sessionId: string, handle: TerminalInstanceHandle | null) => {
    if (handle) {
      instanceRefs.current.set(sessionId, handle)
    } else {
      instanceRefs.current.delete(sessionId)
    }
  }, [])

  // Send a control message for a specific session.
  const sendControl = useCallback((sessionId: string, msg: ControlMessage) => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    const payload = encoder.encode(JSON.stringify(msg))
    ws.send(buildFrame(MSG_CONTROL, sessionId, payload))
  }, [])

  // Send raw input for a session.
  const sendInput = useCallback((sessionId: string, data: Uint8Array) => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(buildFrame(MSG_INPUT, sessionId, data))
  }, [])

  // Handle resize from a terminal instance.
  const handleResize = useCallback((sessionId: string, cols: number, rows: number) => {
    sendControl(sessionId, { type: "resize", cols, rows })
  }, [sendControl])

  // Create a new terminal on a given node.
  const handleCreate = useCallback((nodeId: string) => {
    // Use empty session ID for create — server assigns one.
    sendControl("", { type: "create", node_id: nodeId, cols: 80, rows: 24 })
  }, [sendControl])

  // Close a terminal tab.
  const handleClose = useCallback((tabId: string) => {
    sendControl(tabId, { type: "close" })
    setTabs((prev) => {
      const next = prev.filter((t) => t.id !== tabId)
      // If we closed the active tab, switch to the last remaining.
      setActiveTabId((currentActive) => {
        if (currentActive === tabId) {
          return next.length > 0 ? next[next.length - 1].id : null
        }
        return currentActive
      })
      return next
    })
  }, [sendControl])

  // WebSocket connection.
  useEffect(() => {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
    const ws = new WebSocket(`${proto}//${window.location.host}/ws/terminal`)
    ws.binaryType = "arraybuffer"
    wsRef.current = ws

    ws.onopen = () => {
      setStatus("connected")
    }

    ws.onmessage = (ev) => {
      if (!(ev.data instanceof ArrayBuffer)) return
      const { type, sessionId, payload } = parseFrame(ev.data)

      switch (type) {
        case MSG_OUTPUT: {
          instanceRefs.current.get(sessionId)?.write(payload)
          break
        }
        case MSG_CONTROL: {
          const ctrl: ControlMessage = JSON.parse(decoder.decode(payload))
          handleControlMessage(sessionId, ctrl)
          break
        }
      }
    }

    ws.onclose = () => {
      setStatus("disconnected")
    }

    document.title = "Hiro Terminal"

    return () => {
      ws.close()
      wsRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const handleControlMessage = useCallback((sessionId: string, ctrl: ControlMessage) => {
    switch (ctrl.type) {
      case "created": {
        const newTab: TerminalTab = {
          id: ctrl.session_id || sessionId,
          nodeId: ctrl.node_id || "home",
          nodeName: ctrl.node_name || "local",
        }
        setTabs((prev) => {
          // Don't add duplicates.
          if (prev.some((t) => t.id === newTab.id)) return prev
          return [...prev, newTab]
        })
        // Always activate newly created tabs.
        setActiveTabId(newTab.id)
        break
      }
      case "replay_start":
      case "replay_end":
        // Server sends these to bracket replay data. No special handling needed
        // since replay output is written to xterm the same as live output.
        break
      case "exited": {
        // Shell exited naturally — remove the tab without sending a redundant
        // "close" back to the server (the session is already dead).
        setTabs((prev) => {
          const next = prev.filter((t) => t.id !== sessionId)
          setActiveTabId((cur) =>
            cur === sessionId
              ? (next.length > 0 ? next[next.length - 1].id : null)
              : cur,
          )
          return next
        })
        break
      }
      case "closed": {
        setTabs((prev) => prev.filter((t) => t.id !== (ctrl.session_id || sessionId)))
        break
      }
      case "error": {
        console.error("[terminal]", ctrl.message)
        setError(ctrl.message ?? "Unknown error")
        setTimeout(() => setError(null), 5000)
        break
      }
    }
  }, [])

  return (
    <div className="h-screen w-screen bg-[#282c34] flex flex-col">
      <TerminalTabBar
        tabs={tabs}
        activeTabId={activeTabId}
        onSwitch={setActiveTabId}
        onClose={handleClose}
        onCreate={handleCreate}
      />

      {error && (
        <div className="absolute top-12 right-4 z-20 rounded-md bg-red-900/90 text-red-200 text-xs px-3 py-2 shadow-lg">
          {error}
        </div>
      )}

      {status === "connecting" && tabs.length === 0 && (
        <div className="flex-1 flex items-center justify-center text-[#abb2bf]/50 text-sm">
          Connecting...
        </div>
      )}

      {status === "disconnected" && (
        <div className="flex-1 flex items-center justify-center text-[#abb2bf]/50 text-sm">
          Connection lost. Close and reopen to reconnect.
        </div>
      )}

      {tabs.map((tab) => (
        <TerminalInstance
          key={tab.id}
          ref={(handle) => setInstanceRef(tab.id, handle)}
          sessionId={tab.id}
          visible={tab.id === activeTabId && status === "connected"}
          onData={sendInput}
          onResize={handleResize}
        />
      ))}
    </div>
  )
}
