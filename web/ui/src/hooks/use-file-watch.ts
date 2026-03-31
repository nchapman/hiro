import { useEffect, useRef } from "react"

export type FileChangeOp = "create" | "write" | "remove" | "rename"

export interface FileChangeEvent {
  path: string
  op: FileChangeOp
}

interface FileChangeBatch {
  events: FileChangeEvent[]
}

/**
 * Subscribes to server-sent file change events via SSE.
 * The callback receives batches of debounced filesystem events.
 * Auto-reconnects on error (native EventSource behavior).
 */
export function useFileWatch(onEvents: (events: FileChangeEvent[]) => void) {
  const ref = useRef(onEvents)
  useEffect(() => { ref.current = onEvents }, [onEvents])

  useEffect(() => {
    const es = new EventSource("/api/files/events")

    es.onmessage = (e) => {
      try {
        const batch: FileChangeBatch = JSON.parse(e.data)
        if (batch.events?.length > 0) {
          ref.current(batch.events)
        }
      } catch {
        // Ignore malformed messages.
      }
    }

    return () => es.close()
  }, [])
}
