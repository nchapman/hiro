import { describe, it, expect } from "vitest"
import { extractToolOutput, parseFantasyMessage, parseToolResults, mergeHistoryMessages } from "./chat-parser"
import type { HistoryMessage } from "./chat-types"

describe("extractToolOutput", () => {
  it("returns null for null/undefined input", () => {
    expect(extractToolOutput(null)).toBeNull()
    expect(extractToolOutput(undefined)).toBeNull()
  })

  it("returns null for non-object input", () => {
    expect(extractToolOutput("string")).toBeNull()
    expect(extractToolOutput(42)).toBeNull()
  })

  it("extracts text output", () => {
    const result = extractToolOutput({ type: "text", data: { text: "hello" } })
    expect(result).toEqual({ output: "hello", isError: false })
  })

  it("extracts error output", () => {
    const result = extractToolOutput({ type: "error", data: { error: "failed" } })
    expect(result).toEqual({ output: "failed", isError: true })
  })

  it("returns null for unknown type", () => {
    expect(extractToolOutput({ type: "image", data: {} })).toBeNull()
  })

  it("returns null when data field has wrong type", () => {
    expect(extractToolOutput({ type: "text", data: { text: 123 } })).toBeNull()
    expect(extractToolOutput({ type: "error", data: { error: 123 } })).toBeNull()
  })
})

describe("parseFantasyMessage", () => {
  it("parses text content", () => {
    const msg = JSON.stringify({
      role: "assistant",
      content: [{ type: "text", data: { text: "Hello world" } }],
    })
    const result = parseFantasyMessage(msg)
    expect(result.content).toBe("Hello world")
    expect(result.toolCalls).toHaveLength(0)
    expect(result.thinking).toBe("")
    expect(result.attachments).toHaveLength(0)
  })

  it("concatenates multiple text parts", () => {
    const msg = JSON.stringify({
      role: "assistant",
      content: [
        { type: "text", data: { text: "Hello " } },
        { type: "text", data: { text: "world" } },
      ],
    })
    const result = parseFantasyMessage(msg)
    expect(result.content).toBe("Hello world")
  })

  it("parses reasoning content", () => {
    const msg = JSON.stringify({
      role: "assistant",
      content: [
        { type: "reasoning", data: { text: "Let me think..." } },
        { type: "text", data: { text: "The answer is 42" } },
      ],
    })
    const result = parseFantasyMessage(msg)
    expect(result.thinking).toBe("Let me think...")
    expect(result.content).toBe("The answer is 42")
  })

  it("parses file attachments", () => {
    const msg = JSON.stringify({
      role: "assistant",
      content: [
        {
          type: "file",
          data: { filename: "test.png", media_type: "image/png", data: "base64data" },
        },
      ],
    })
    const result = parseFantasyMessage(msg)
    expect(result.attachments).toHaveLength(1)
    expect(result.attachments[0]).toEqual({
      filename: "test.png",
      media_type: "image/png",
      data: "base64data",
    })
  })

  it("parses tool calls", () => {
    const msg = JSON.stringify({
      role: "assistant",
      content: [
        {
          type: "tool-call",
          data: { tool_call_id: "tc1", tool_name: "bash", input: "ls -la", status: "running" },
        },
      ],
    })
    const result = parseFantasyMessage(msg)
    expect(result.toolCalls).toHaveLength(1)
    expect(result.toolCalls[0]).toMatchObject({
      id: "tc1",
      name: "bash",
      input: "ls -la",
      status: "running",
    })
  })

  it("merges tool results into matching tool calls", () => {
    const msg = JSON.stringify({
      role: "assistant",
      content: [
        { type: "tool-call", data: { tool_call_id: "tc1", tool_name: "bash" } },
        {
          type: "tool-result",
          data: { tool_call_id: "tc1", output: { type: "text", data: { text: "file.txt" } } },
        },
      ],
    })
    const result = parseFantasyMessage(msg)
    expect(result.toolCalls).toHaveLength(1)
    expect(result.toolCalls[0].output).toBe("file.txt")
    expect(result.toolCalls[0].isError).toBe(false)
  })

  it("handles tool result errors", () => {
    const msg = JSON.stringify({
      role: "assistant",
      content: [
        { type: "tool-call", data: { tool_call_id: "tc1", tool_name: "bash" } },
        {
          type: "tool-result",
          data: { tool_call_id: "tc1", output: { type: "error", data: { error: "command failed" } } },
        },
      ],
    })
    const result = parseFantasyMessage(msg)
    expect(result.toolCalls[0].output).toBe("command failed")
    expect(result.toolCalls[0].isError).toBe(true)
  })

  it("returns empty result for invalid JSON", () => {
    const result = parseFantasyMessage("not json")
    expect(result.content).toBe("")
    expect(result.toolCalls).toHaveLength(0)
    expect(result.thinking).toBe("")
    expect(result.attachments).toHaveLength(0)
  })

  it("defaults file attachment fields", () => {
    const msg = JSON.stringify({
      role: "assistant",
      content: [{ type: "file", data: {} }],
    })
    const result = parseFantasyMessage(msg)
    expect(result.attachments[0].filename).toBe("file")
    expect(result.attachments[0].media_type).toBe("")
  })
})

describe("parseToolResults", () => {
  it("extracts tool results from a message", () => {
    const msg = JSON.stringify({
      role: "tool",
      content: [
        {
          type: "tool-result",
          data: { tool_call_id: "tc1", output: { type: "text", data: { text: "output1" } } },
        },
        {
          type: "tool-result",
          data: { tool_call_id: "tc2", output: { type: "error", data: { error: "err" } } },
        },
      ],
    })
    const results = parseToolResults(msg)
    expect(results.size).toBe(2)
    expect(results.get("tc1")).toEqual({ output: "output1", isError: false })
    expect(results.get("tc2")).toEqual({ output: "err", isError: true })
  })

  it("returns empty map for invalid JSON", () => {
    expect(parseToolResults("bad json").size).toBe(0)
  })

  it("skips non-tool-result parts", () => {
    const msg = JSON.stringify({
      role: "tool",
      content: [
        { type: "text", data: { text: "ignored" } },
        {
          type: "tool-result",
          data: { tool_call_id: "tc1", output: { type: "text", data: { text: "kept" } } },
        },
      ],
    })
    const results = parseToolResults(msg)
    expect(results.size).toBe(1)
  })
})

describe("mergeHistoryMessages", () => {
  it("converts simple user and assistant messages", () => {
    const history: HistoryMessage[] = [
      { role: "user", content: "Hello" },
      { role: "assistant", content: "Hi there" },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(2)
    expect(messages[0].role).toBe("user")
    expect(messages[0].content).toBe("Hello")
    expect(messages[1].role).toBe("assistant")
    expect(messages[1].content).toBe("Hi there")
  })

  it("merges assistant messages with tool calls and results", () => {
    const assistantRaw = JSON.stringify({
      role: "assistant",
      content: [
        { type: "tool-call", data: { tool_call_id: "tc1", tool_name: "bash" } },
        { type: "text", data: { text: "Running command..." } },
      ],
    })
    const toolRaw = JSON.stringify({
      role: "tool",
      content: [
        {
          type: "tool-result",
          data: { tool_call_id: "tc1", output: { type: "text", data: { text: "done" } } },
        },
      ],
    })
    const finalRaw = JSON.stringify({
      role: "assistant",
      content: [{ type: "text", data: { text: "All done!" } }],
    })

    const history: HistoryMessage[] = [
      { role: "assistant", content: "Running command...", raw_json: assistantRaw },
      { role: "tool", content: "", raw_json: toolRaw },
      { role: "assistant", content: "All done!", raw_json: finalRaw },
    ]

    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(1)
    expect(messages[0].toolCalls).toHaveLength(1)
    expect(messages[0].toolCalls![0].output).toBe("done")
    expect(messages[0].content).toContain("Running command...")
    expect(messages[0].content).toContain("All done!")
  })

  it("extracts user attachments from raw_json", () => {
    const userRaw = JSON.stringify({
      role: "user",
      content: [
        { type: "text", data: { text: "Check this" } },
        { type: "file", data: { filename: "img.png", media_type: "image/png", data: "abc" } },
      ],
    })
    const history: HistoryMessage[] = [
      { role: "user", content: "Check this", raw_json: userRaw },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(1)
    expect(messages[0].attachments).toHaveLength(1)
    expect(messages[0].attachments![0].filename).toBe("img.png")
  })

  it("handles empty history", () => {
    expect(mergeHistoryMessages([])).toEqual([])
  })

  it("filters out meta messages (system reminders)", () => {
    const history: HistoryMessage[] = [
      { role: "user", content: "<system-reminder>secrets list</system-reminder>", is_meta: true },
      { role: "user", content: "Hello" },
      { role: "assistant", content: "Hi there" },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(2)
    expect(messages[0].content).toBe("Hello")
    expect(messages[1].content).toBe("Hi there")
  })

  it("ignores orphaned tool-result with no preceding assistant message", () => {
    const toolRaw = JSON.stringify({
      role: "tool",
      content: [
        { type: "tool-result", data: { tool_call_id: "tc1", output: { type: "text", data: { text: "x" } } } },
      ],
    })
    const history: HistoryMessage[] = [{ role: "tool", content: "", raw_json: toolRaw }]
    expect(mergeHistoryMessages(history)).toEqual([])
  })

  it("leaves tool calls without matching result as undefined", () => {
    const assistantRaw = JSON.stringify({
      role: "assistant",
      content: [
        { type: "tool-call", data: { tool_call_id: "tc1", tool_name: "bash" } },
      ],
    })
    const toolRaw = JSON.stringify({
      role: "tool",
      content: [
        { type: "tool-result", data: { tool_call_id: "tc-unmatched", output: { type: "text", data: { text: "x" } } } },
      ],
    })
    const finalRaw = JSON.stringify({
      role: "assistant",
      content: [{ type: "text", data: { text: "done" } }],
    })
    const history: HistoryMessage[] = [
      { role: "assistant", content: "", raw_json: assistantRaw },
      { role: "tool", content: "", raw_json: toolRaw },
      { role: "assistant", content: "done", raw_json: finalRaw },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(1)
    expect(messages[0].toolCalls![0].output).toBeUndefined()
    expect(messages[0].toolCalls![0].isError).toBeUndefined()
  })

  it("parses agent notification meta messages into notification blocks", () => {
    const history: HistoryMessage[] = [
      { role: "user", content: "spawn an agent", is_meta: false },
      {
        role: "user",
        content: '<agent-notification>\n<agent>assistant</agent>\n<status>completed</status>\n<summary>Agent "assistant" finished</summary>\n<result>hostname is hiro-minimax</result>\n</agent-notification>',
        is_meta: true,
      },
      { role: "assistant", content: "The hostname is hiro-minimax." },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(2)
    expect(messages[0].content).toBe("spawn an agent")
    // Notification is attached to the assistant message
    expect(messages[1].notifications).toHaveLength(1)
    expect(messages[1].notifications![0].agent).toBe("assistant")
    expect(messages[1].notifications![0].status).toBe("completed")
    expect(messages[1].notifications![0].result).toBe("hostname is hiro-minimax")
    expect(messages[1].content).toBe("The hostname is hiro-minimax.")
  })

  it("parses failed agent notification with empty result", () => {
    const history: HistoryMessage[] = [
      {
        role: "user",
        content: '<agent-notification>\n<agent>assistant</agent>\n<status>failed</status>\n<summary>Agent "assistant" failed: node not found</summary>\n<result></result>\n</agent-notification>',
        is_meta: true,
      },
      { role: "assistant", content: "The spawn failed." },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(1)
    expect(messages[0].notifications).toHaveLength(1)
    expect(messages[0].notifications![0].status).toBe("failed")
    expect(messages[0].notifications![0].result).toBe("")
    expect(messages[0].content).toBe("The spawn failed.")
  })

  it("handles multiple consecutive agent notifications", () => {
    const notif = (agent: string) =>
      `<agent-notification>\n<agent>${agent}</agent>\n<status>completed</status>\n<summary>Agent "${agent}" finished</summary>\n<result>done</result>\n</agent-notification>`
    const history: HistoryMessage[] = [
      { role: "user", content: notif("a"), is_meta: true },
      { role: "user", content: notif("b"), is_meta: true },
      { role: "assistant", content: "Both done." },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(1)
    expect(messages[0].notifications).toHaveLength(2)
    expect(messages[0].notifications![0].agent).toBe("a")
    expect(messages[0].notifications![1].agent).toBe("b")
  })

  it("notification-only history (no following assistant message) flushes correctly", () => {
    const history: HistoryMessage[] = [
      {
        role: "user",
        content: '<agent-notification>\n<agent>x</agent>\n<status>completed</status>\n<summary>done</summary>\n<result>ok</result>\n</agent-notification>',
        is_meta: true,
      },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(1)
    expect(messages[0].role).toBe("assistant")
    expect(messages[0].notifications).toHaveLength(1)
    expect(messages[0].content).toBe("")
  })

  it("still filters non-notification meta messages", () => {
    const history: HistoryMessage[] = [
      { role: "user", content: "<system-reminder>secrets</system-reminder>", is_meta: true },
      { role: "assistant", content: "meta response", is_meta: true },
      { role: "user", content: "Hello" },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(1)
    expect(messages[0].content).toBe("Hello")
  })

  it("flushes pending current message at end of history", () => {
    const raw = JSON.stringify({
      role: "assistant",
      content: [
        { type: "reasoning", data: { text: "thinking..." } },
        { type: "text", data: { text: "result" } },
      ],
    })
    const history: HistoryMessage[] = [
      { role: "assistant", content: "result", raw_json: raw },
    ]
    const messages = mergeHistoryMessages(history)
    expect(messages).toHaveLength(1)
    expect(messages[0].thinking).toBe("thinking...")
    expect(messages[0].content).toBe("result")
  })
})
