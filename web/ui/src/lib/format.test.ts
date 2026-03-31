import { describe, it, expect } from "vitest"
import { formatTokenCount, formatCost } from "./format"

describe("formatTokenCount", () => {
  it("formats millions", () => {
    expect(formatTokenCount(1_000_000)).toBe("1.0M")
    expect(formatTokenCount(2_500_000)).toBe("2.5M")
    expect(formatTokenCount(10_300_000)).toBe("10.3M")
  })

  it("formats thousands", () => {
    expect(formatTokenCount(1_000)).toBe("1.0k")
    expect(formatTokenCount(1_500)).toBe("1.5k")
    expect(formatTokenCount(999_999)).toBe("1000.0k")
  })

  it("formats small numbers as-is", () => {
    expect(formatTokenCount(0)).toBe("0")
    expect(formatTokenCount(1)).toBe("1")
    expect(formatTokenCount(999)).toBe("999")
  })
})

describe("formatCost", () => {
  it("formats small costs with 4 decimal places", () => {
    expect(formatCost(0.001)).toBe("$0.0010")
    expect(formatCost(0.0099)).toBe("$0.0099")
    expect(formatCost(0)).toBe("$0.0000")
  })

  it("formats larger costs with 2 decimal places", () => {
    expect(formatCost(0.01)).toBe("$0.01")
    expect(formatCost(1.5)).toBe("$1.50")
    expect(formatCost(99.99)).toBe("$99.99")
  })
})
