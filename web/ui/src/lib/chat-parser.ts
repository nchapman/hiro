import type { ToolCall, Message, MessageAttachment, AgentNotification, HistoryMessage, FantasyMessage } from "./chat-types"

export function extractToolOutput(raw: unknown): { output: string; isError: boolean } | null {
  if (!raw || typeof raw !== "object") return null
  const obj = raw as { type?: string; data?: Record<string, unknown> }
  if (obj.type === "text" && typeof obj.data?.text === "string") {
    return { output: obj.data.text, isError: false }
  }
  if (obj.type === "error" && typeof obj.data?.error === "string") {
    return { output: obj.data.error, isError: true }
  }
  return null
}

export function parseFantasyMessage(rawJSON: string): { content: string; toolCalls: ToolCall[]; thinking: string; attachments: MessageAttachment[] } {
  try {
    const msg: FantasyMessage = JSON.parse(rawJSON)
    const textParts: string[] = []
    const thinkingParts: string[] = []
    const toolCalls: ToolCall[] = []
    const attachments: MessageAttachment[] = []

    for (const part of msg.content) {
      switch (part.type) {
        case "text":
          if (typeof part.data.text === "string") textParts.push(part.data.text)
          break
        case "reasoning":
          if (typeof part.data.text === "string") thinkingParts.push(part.data.text)
          break
        case "file":
          attachments.push({
            filename: (part.data.filename as string) || "file",
            media_type: (part.data.media_type as string) || "",
            data: part.data.data as string | undefined,
          })
          break
        case "tool-call":
          toolCalls.push({
            id: (part.data.tool_call_id as string) || crypto.randomUUID(),
            name: (part.data.tool_name as string) || "unknown",
            input: part.data.input as string | undefined,
            status: part.data.status as string | undefined,
          })
          break
        case "tool-result": {
          const callID = part.data.tool_call_id as string
          const result = extractToolOutput(part.data.output)
          if (callID && result) {
            const tc = toolCalls.find((t) => t.id === callID)
            if (tc) { tc.output = result.output; tc.isError = result.isError }
          }
          break
        }
      }
    }

    return { content: textParts.join(""), toolCalls, thinking: thinkingParts.join(""), attachments }
  } catch {
    return { content: "", toolCalls: [], thinking: "", attachments: [] }
  }
}

export function parseToolResults(rawJSON: string): Map<string, { output: string; isError: boolean }> {
  const results = new Map<string, { output: string; isError: boolean }>()
  try {
    const msg: FantasyMessage = JSON.parse(rawJSON)
    for (const part of msg.content) {
      if (part.type === "tool-result") {
        const callID = part.data.tool_call_id as string
        const result = extractToolOutput(part.data.output)
        if (callID && result) {
          results.set(callID, result)
        }
      }
    }
  } catch { /* ignore */ }
  return results
}

function parseAgentNotification(content: string): AgentNotification | null {
  const match = content.match(/<agent-notification>([\s\S]*?)<\/agent-notification>/)
  if (!match) return null
  const xml = match[1]
  const agent = xml.match(/<agent>([\s\S]*?)<\/agent>/)?.[1]?.trim() ?? ""
  const status = xml.match(/<status>([\s\S]*?)<\/status>/)?.[1]?.trim() ?? ""
  const summary = xml.match(/<summary>([\s\S]*?)<\/summary>/)?.[1]?.trim() ?? ""
  const result = xml.match(/<result>([\s\S]*?)<\/result>/)?.[1]?.trim() ?? ""
  return { agent, status, summary, result }
}

export function mergeHistoryMessages(history: HistoryMessage[]): Message[] {
  const messages: Message[] = []
  let current: Message | undefined

  function flushCurrent() {
    if (current) {
      messages.push(current)
      current = undefined
    }
  }

  for (const m of history) {
    // Show agent notifications as expandable blocks; skip other meta messages.
    if (m.is_meta) {
      if (m.role === "user") {
        const notif = parseAgentNotification(m.content)
        if (notif) {
          // Attach to current assistant message or create one.
          if (!current) {
            current = { id: crypto.randomUUID(), role: "assistant", content: "", toolCalls: [] }
          }
          current.notifications = [...(current.notifications ?? []), notif]
        }
      }
      continue
    }

    if (m.role === "tool" && m.raw_json) {
      if (current) {
        const results = parseToolResults(m.raw_json)
        for (const [callID, result] of results) {
          const target = current.toolCalls?.find((t) => t.id === callID)
          if (target) {
            target.output = result.output
            target.isError = result.isError
          }
        }
      }
      continue
    }

    if (m.role === "assistant" && m.raw_json) {
      const parsed = parseFantasyMessage(m.raw_json)
      if (parsed.toolCalls.length > 0 || parsed.thinking) {
        if (!current) {
          current = { id: crypto.randomUUID(), role: "assistant", content: "", toolCalls: [] }
        }
        if (parsed.toolCalls.length > 0) {
          current.toolCalls = [...(current.toolCalls ?? []), ...parsed.toolCalls]
        }
        if (parsed.thinking) {
          current.thinking = (current.thinking || "") + parsed.thinking
        }
        if (parsed.content) current.content += parsed.content
        continue
      }
      if (current) {
        if (parsed.content) current.content += parsed.content
        else current.content += m.content
        flushCurrent()
        continue
      }
    }

    // If current has pending notifications, merge a plain assistant message into it.
    if (m.role === "assistant" && current?.notifications?.length) {
      current.content += m.content
      flushCurrent()
      continue
    }

    flushCurrent()
    let userAttachments: MessageAttachment[] | undefined
    if (m.role === "user" && m.raw_json) {
      const parsed = parseFantasyMessage(m.raw_json)
      if (parsed.attachments.length > 0) userAttachments = parsed.attachments
    }
    messages.push({
      id: crypto.randomUUID(),
      role: m.role as Message["role"],
      content: m.content,
      attachments: userAttachments,
    })
  }

  flushCurrent()
  return messages
}
