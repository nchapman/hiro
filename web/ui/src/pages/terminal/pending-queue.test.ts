import { describe, it, expect, vi } from "vitest"
import { FrameQueue, applyFrame } from "./pending-queue"
import type { TerminalInstanceHandle } from "./TerminalInstance"

function mockHandle(): TerminalInstanceHandle & { calls: unknown[] } {
  const calls: unknown[] = []
  return {
    calls,
    write: vi.fn((data: Uint8Array) => calls.push({ op: "write", data })),
    writeString: vi.fn((s: string) => calls.push({ op: "writeString", s })),
    fit: vi.fn(() => calls.push({ op: "fit" })),
    setReplaying: vi.fn((r: boolean) => calls.push({ op: "setReplaying", r })),
  }
}

describe("applyFrame", () => {
  it("dispatches output frames to write()", () => {
    const h = mockHandle()
    const data = new Uint8Array([1, 2, 3])
    applyFrame(h, { kind: "output", data })
    expect(h.calls).toEqual([{ op: "write", data }])
  })

  it("dispatches replay markers to setReplaying()", () => {
    const h = mockHandle()
    applyFrame(h, { kind: "replay_start" })
    applyFrame(h, { kind: "replay_end" })
    expect(h.calls).toEqual([
      { op: "setReplaying", r: true },
      { op: "setReplaying", r: false },
    ])
  })
})

describe("FrameQueue", () => {
  it("delivers directly when a handle is registered", () => {
    const q = new FrameQueue()
    const h = mockHandle()
    q.register("s1", h)
    q.deliver("s1", { kind: "output", data: new Uint8Array([1]) })
    expect(h.calls).toEqual([{ op: "write", data: new Uint8Array([1]) }])
    expect(q.pendingCount("s1")).toBe(0)
  })

  it("buffers frames that arrive before the handle registers", () => {
    const q = new FrameQueue()
    q.deliver("s1", { kind: "replay_start" })
    q.deliver("s1", { kind: "output", data: new Uint8Array([65]) })
    q.deliver("s1", { kind: "replay_end" })
    expect(q.pendingCount("s1")).toBe(3)

    const h = mockHandle()
    q.register("s1", h)
    // Drained in order: replay_start → output → replay_end.
    expect(h.calls).toEqual([
      { op: "setReplaying", r: true },
      { op: "write", data: new Uint8Array([65]) },
      { op: "setReplaying", r: false },
    ])
    expect(q.pendingCount("s1")).toBe(0)
  })

  it("preserves exact ordering across many frames", () => {
    const q = new FrameQueue()
    const frames: Array<Parameters<FrameQueue["deliver"]>[1]> = []
    for (let i = 0; i < 50; i++) {
      frames.push({ kind: "output", data: new Uint8Array([i]) })
    }
    for (const f of frames) q.deliver("s1", f)
    const h = mockHandle()
    q.register("s1", h)
    const writes = (h.calls as Array<{ op: string; data?: Uint8Array }>)
      .filter((c) => c.op === "write")
      .map((c) => c.data![0])
    expect(writes).toEqual([...Array(50).keys()])
  })

  it("isolates queues between sessions", () => {
    const q = new FrameQueue()
    q.deliver("a", { kind: "output", data: new Uint8Array([1]) })
    q.deliver("b", { kind: "output", data: new Uint8Array([2]) })
    const ha = mockHandle()
    q.register("a", ha)
    expect(ha.calls).toEqual([{ op: "write", data: new Uint8Array([1]) }])
    expect(q.pendingCount("b")).toBe(1)
  })

  it("drops buffered frames on unregister", () => {
    const q = new FrameQueue()
    q.deliver("s1", { kind: "output", data: new Uint8Array([1]) })
    q.register("s1", null)
    expect(q.pendingCount("s1")).toBe(0)
    // A later re-register with a handle must not see the dropped frames.
    const h = mockHandle()
    q.register("s1", h)
    expect(h.calls).toEqual([])
  })

  it("unregister with null also drops the live handle", () => {
    const q = new FrameQueue()
    const h = mockHandle()
    q.register("s1", h)
    q.register("s1", null)
    // Subsequent deliver() should buffer, not route to the old handle.
    q.deliver("s1", { kind: "output", data: new Uint8Array([9]) })
    expect(h.calls).toEqual([])
    expect(q.pendingCount("s1")).toBe(1)
  })

  it("does not double-apply a frame if deliver() re-enters during drain", () => {
    // If applyFrame somehow triggered another deliver() for the same session,
    // the drain must have already removed queued frames to prevent duplicates.
    const q = new FrameQueue()
    q.deliver("s1", { kind: "replay_start" })
    q.deliver("s1", { kind: "output", data: new Uint8Array([1]) })

    let reentered = false
    const h: TerminalInstanceHandle = {
      write: vi.fn(),
      writeString: vi.fn(),
      fit: vi.fn(),
      setReplaying: vi.fn(() => {
        if (!reentered) {
          reentered = true
          // Re-entrant delivery during drain — should route directly to the
          // just-registered handle, not get stuck in a stale queue.
          q.deliver("s1", { kind: "output", data: new Uint8Array([2]) })
        }
      }),
    }
    q.register("s1", h)

    expect((h.write as ReturnType<typeof vi.fn>).mock.calls.map((c) => (c[0] as Uint8Array)[0]))
      .toEqual([2, 1])
    // The re-entrant deliver arrives during setReplaying, before the queued
    // output is drained — so ordering is [2, then the originally queued 1].
    // The key property we're verifying is that no frame is delivered twice.
    expect((h.write as ReturnType<typeof vi.fn>).mock.calls).toHaveLength(2)
  })
})
