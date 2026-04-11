export interface ModelInfo {
  id: string
  name: string
  provider?: string
  can_reason: boolean
  reasoning_levels?: string[]
  context_window: number
}

export interface ToolCall {
  id: string
  name: string
  input?: string
  output?: string
  isError?: boolean
  status?: string
}

export interface MessageAttachment {
  filename: string
  media_type: string
  data?: string
}

export interface AgentNotification {
  agent: string
  status: string
  summary: string
  result: string
}

export interface Message {
  id: string
  role: "user" | "assistant" | "system"
  content: string
  toolCalls?: ToolCall[]
  notifications?: AgentNotification[]
  thinking?: string
  isThinking?: boolean
  attachments?: MessageAttachment[]
}

export interface PendingAttachment {
  id: string
  file: File
  preview?: string
  dataBase64: string
  mediaType: string
}

export interface HistoryMessage {
  role: "user" | "assistant" | "tool"
  content: string
  raw_json?: string
  is_meta?: boolean
  timestamp?: string
}

export interface FantasyMessage {
  role: string
  content: Array<{ type: string; data: Record<string, unknown> }>
}
