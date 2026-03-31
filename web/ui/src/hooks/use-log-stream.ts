import { useEffect, useRef } from "react"

export interface LogEntry {
  id: number
  time: string
  level: "DEBUG" | "INFO" | "WARN" | "ERROR"
  message: string
  component?: string
  instance_id?: string
  attrs?: Record<string, unknown>
}

/**
 * Subscribes to real-time log entries via Server-Sent Events.
 * Each SSE message contains a single JSON-encoded LogEntry.
 * Auto-reconnects on error (native EventSource behavior).
 */
export function useLogStream(
  onLog: (entry: LogEntry) => void,
  enabled: boolean = true,
) {
  const ref = useRef(onLog)
  ref.current = onLog

  useEffect(() => {
    if (!enabled) return

    const es = new EventSource("/api/logs/stream")

    es.onmessage = (e) => {
      try {
        const entry: LogEntry = JSON.parse(e.data)
        ref.current(entry)
      } catch {
        // Ignore malformed messages.
      }
    }

    return () => es.close()
  }, [enabled])
}
