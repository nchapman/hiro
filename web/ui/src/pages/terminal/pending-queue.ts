import type { TerminalInstanceHandle } from "./TerminalInstance"

// Frames for an xterm session that can be delayed: terminal output and the
// replay-bracket markers. `created` / `exited` / `closed` are handled at the
// React-state layer and don't belong here.
export type PendingFrame =
  | { kind: "output"; data: Uint8Array }
  | { kind: "replay_start" }
  | { kind: "replay_end" }

export function applyFrame(handle: TerminalInstanceHandle, frame: PendingFrame) {
  switch (frame.kind) {
    case "output":
      handle.write(frame.data)
      break
    case "replay_start":
      handle.setReplaying(true)
      break
    case "replay_end":
      handle.setReplaying(false)
      break
  }
}

// FrameQueue routes session frames to their TerminalInstance handles, buffering
// any frames that arrive before the matching handle has been registered.
//
// The server emits `created` then immediately `replay_start` / output /
// `replay_end` as separate WebSocket frames. React doesn't commit the new
// TerminalInstance (and fire its ref callback) until after the current JS
// task, so replay frames can arrive in the interim. Without this queue they
// would be delivered to `undefined` and silently lost.
export class FrameQueue {
  private readonly handles = new Map<string, TerminalInstanceHandle>()
  private readonly pending = new Map<string, PendingFrame[]>()

  deliver(sessionId: string, frame: PendingFrame): void {
    const handle = this.handles.get(sessionId)
    if (handle) {
      applyFrame(handle, frame)
      return
    }
    const list = this.pending.get(sessionId)
    if (list) {
      list.push(frame)
    } else {
      this.pending.set(sessionId, [frame])
    }
  }

  // Register or unregister a handle. Passing null drops both the handle and
  // any pending frames (the session is going away with the component).
  register(sessionId: string, handle: TerminalInstanceHandle | null): void {
    if (!handle) {
      this.handles.delete(sessionId)
      this.pending.delete(sessionId)
      return
    }
    this.handles.set(sessionId, handle)
    const queued = this.pending.get(sessionId)
    if (!queued) return
    // Delete before applying so re-entrant deliver() calls from within
    // applyFrame don't re-read a stale queue.
    this.pending.delete(sessionId)
    for (const frame of queued) applyFrame(handle, frame)
  }

  // Visible for tests only.
  pendingCount(sessionId: string): number {
    return this.pending.get(sessionId)?.length ?? 0
  }
}
