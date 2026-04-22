import { describe, it, expect, vi, afterEach } from "vitest"
import { cn, randomId } from "./utils"

describe("cn", () => {
  it("merges class names", () => {
    expect(cn("foo", "bar")).toBe("foo bar")
  })

  it("handles conditional classes", () => {
    const condition = false
    expect(cn("base", condition && "hidden", "extra")).toBe("base extra")
  })

  it("resolves tailwind conflicts (last wins)", () => {
    expect(cn("px-2", "px-4")).toBe("px-4")
    expect(cn("text-red-500", "text-blue-500")).toBe("text-blue-500")
  })

  it("handles empty/undefined inputs", () => {
    expect(cn("", undefined, null, "visible")).toBe("visible")
  })
})

describe("randomId", () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("uses crypto.randomUUID when available", () => {
    const spy = vi.spyOn(crypto, "randomUUID").mockReturnValue("00000000-0000-0000-0000-000000000000")
    expect(randomId()).toBe("00000000-0000-0000-0000-000000000000")
    expect(spy).toHaveBeenCalledOnce()
  })

  it("falls back when crypto.randomUUID is unavailable (insecure context)", () => {
    const original = crypto.randomUUID
    Object.defineProperty(crypto, "randomUUID", { value: undefined, configurable: true })
    try {
      const id = randomId()
      expect(id).toMatch(/^id-[a-z0-9]+-[a-z0-9]+$/)
    } finally {
      Object.defineProperty(crypto, "randomUUID", { value: original, configurable: true })
    }
  })

  it("returns unique values across calls", () => {
    const ids = new Set(Array.from({ length: 100 }, () => randomId()))
    expect(ids.size).toBe(100)
  })
})
