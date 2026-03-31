import { describe, it, expect } from "vitest"
import { statusDotColor } from "./session-utils"
import type { SessionInfo } from "@/App"

function makeSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id: "test-id",
    name: "test",
    mode: "persistent",
    status: "running",
    ...overrides,
  }
}

describe("statusDotColor", () => {
  it("returns gray for stopped sessions", () => {
    expect(statusDotColor(makeSession({ status: "stopped" }))).toBe("bg-gray-400")
  })

  it("returns gray for stopped ephemeral sessions", () => {
    expect(statusDotColor(makeSession({ status: "stopped", mode: "ephemeral" }))).toBe("bg-gray-400")
  })

  it("returns violet for running ephemeral sessions", () => {
    expect(statusDotColor(makeSession({ mode: "ephemeral", status: "running" }))).toBe("bg-violet-500")
  })

  it("returns green for running persistent sessions", () => {
    expect(statusDotColor(makeSession({ mode: "persistent", status: "running" }))).toBe("bg-green-500")
  })

  it("returns green for running coordinator sessions", () => {
    expect(statusDotColor(makeSession({ mode: "coordinator", status: "running" }))).toBe("bg-green-500")
  })
})
