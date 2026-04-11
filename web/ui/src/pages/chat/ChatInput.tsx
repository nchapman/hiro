import { useState, useRef, useCallback } from "react"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { HugeiconsIcon } from "@hugeicons/react"
import {
  ArrowUp01Icon,
  ArrowRight01Icon,
  Attachment01Icon,
  Cancel01Icon,
  File02Icon,
} from "@hugeicons/core-free-icons"
import { cn } from "@/lib/utils"
import {
  PromptInput,
  PromptInputTextarea,
} from "@/components/prompt-kit/prompt-input"
import type { ModelInfo, PendingAttachment, MessageAttachment } from "@/lib/chat-types"
import { isImageType } from "@/lib/file-utils"
import type { ChatAttachment } from "@/hooks/use-websocket"

// --- File processing ---

const MAX_ATTACHMENT_SIZE = 5 * 1024 * 1024 // 5 MB
const MAX_ATTACHMENTS = 10

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const result = reader.result as string
      const base64 = result.split(",")[1] || ""
      resolve(base64)
    }
    reader.onerror = reject
    reader.readAsDataURL(file)
  })
}

async function processFiles(files: FileList | File[]): Promise<PendingAttachment[]> {
  const result: PendingAttachment[] = []
  for (const file of Array.from(files)) {
    if (file.size > MAX_ATTACHMENT_SIZE) continue
    const dataBase64 = await fileToBase64(file)
    const att: PendingAttachment = {
      id: crypto.randomUUID(),
      file,
      dataBase64,
      mediaType: file.type || "application/octet-stream",
    }
    if (isImageType(file.type)) {
      att.preview = `data:${file.type};base64,${dataBase64}`
    }
    result.push(att)
  }
  return result
}

// --- Reasoning control ---

function formatEffort(effort: string): string {
  if (effort === "xhigh") return "X-High"
  return effort.charAt(0).toUpperCase() + effort.slice(1)
}

function ReasoningControl({
  model,
  effort,
  onChange,
}: {
  model: ModelInfo | undefined
  effort: string
  onChange: (effort: string) => void
}) {
  if (!model?.can_reason) return null

  const levels = model.reasoning_levels

  // Models with controllable levels: popover with options.
  if (levels?.length) {
    const options = [
      { value: "", label: "Auto" },
      ...levels.map((l) => ({ value: l, label: formatEffort(l) })),
    ]
    const buttonLabel = effort ? `Reason: ${formatEffort(effort)}` : "Reason"

    return (
      <Popover>
        <PopoverTrigger
          render={
            <Button
              variant={effort ? "outline" : "ghost"}
              size="sm"
              className={cn(
                "h-6 gap-1 rounded-full px-2 text-xs",
                effort ? "border-primary/50 bg-primary/10 text-primary" : "text-muted-foreground"
              )}
            />
          }
        >
          {buttonLabel}
          <HugeiconsIcon icon={ArrowRight01Icon} className="h-3 w-3" />
        </PopoverTrigger>
        <PopoverContent align="end" className="w-40 p-1">
          {options.map((o) => (
            <button
              key={o.value}
              onClick={() => onChange(o.value)}
              className={cn(
                "flex w-full rounded-md px-2 py-1.5 text-xs cursor-pointer hover:bg-accent",
                o.value === effort && "bg-accent"
              )}
            >
              {o.label}
            </button>
          ))}
        </PopoverContent>
      </Popover>
    )
  }

  // Binary toggle for models without controllable levels.
  return (
    <Button
      variant={effort ? "outline" : "ghost"}
      size="sm"
      className={cn(
        "h-6 rounded-full px-2 text-xs",
        effort ? "border-primary/50 bg-primary/10 text-primary" : "text-muted-foreground"
      )}
      onClick={(e) => { e.stopPropagation(); onChange(effort ? "" : "on") }}
    >
      {effort ? "Reasoning" : "Reason"}
    </Button>
  )
}

// --- Chat input area ---

export interface ChatInputProps {
  sessionName: string
  connected: boolean
  streaming: boolean
  currentModelInfo: ModelInfo | undefined
  reasoningEffort: string
  onReasoningChange: (effort: string) => void
  onSend: (text: string, attachments: ChatAttachment[] | undefined, displayAttachments: MessageAttachment[]) => void
}

export default function ChatInputArea({
  sessionName,
  connected,
  streaming,
  currentModelInfo,
  reasoningEffort,
  onReasoningChange,
  onSend,
}: ChatInputProps) {
  const [input, setInput] = useState("")
  const [attachments, setAttachments] = useState<PendingAttachment[]>([])
  const fileInputRef = useRef<HTMLInputElement>(null)

  const addAttachments = useCallback(async (files: FileList | File[]) => {
    const processed = await processFiles(files)
    setAttachments((prev) => {
      const combined = [...prev, ...processed]
      return combined.slice(0, MAX_ATTACHMENTS)
    })
  }, [])

  const removeAttachment = useCallback((id: string) => {
    setAttachments((prev) => prev.filter((a) => a.id !== id))
  }, [])

  const handlePaste = useCallback((e: React.ClipboardEvent) => {
    const files = e.clipboardData?.files
    if (files && files.length > 0) {
      const hasFiles = Array.from(files).some((f) => f.type.startsWith("image/"))
      if (hasFiles) {
        e.preventDefault()
        addAttachments(files)
      }
    }
  }, [addAttachments])

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    if (e.dataTransfer.files.length > 0) {
      addAttachments(e.dataTransfer.files)
    }
  }, [addAttachments])

  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault()
  }, [])

  const handleSend = useCallback(() => {
    const text = input.trim()
    const hasAttachments = attachments.length > 0
    if ((!text && !hasAttachments) || streaming || !connected) return

    const displayAttachments: MessageAttachment[] = attachments.map((a) => ({
      filename: a.file.name,
      media_type: a.mediaType,
      data: a.preview ? a.dataBase64 : undefined,
    }))

    const wireAttachments: ChatAttachment[] | undefined = hasAttachments
      ? attachments.map((a) => ({
          filename: a.file.name,
          data: a.dataBase64,
          media_type: a.mediaType,
        }))
      : undefined

    onSend(text, wireAttachments, displayAttachments)
    setInput("")
    setAttachments([])
  }, [input, attachments, streaming, connected, onSend])

  return (
    <div className="mx-auto w-full max-w-3xl px-4 pb-4 pt-2">
      <div
        onDrop={handleDrop}
        onDragOver={handleDragOver}
      >
        <PromptInput
          value={input}
          onValueChange={setInput}
          onSubmit={handleSend}
          isLoading={streaming}
          disabled={!connected}
        >
          {attachments.length > 0 && (
            <div className="flex flex-wrap gap-2 px-3 pt-3">
              {attachments.map((att) => (
                <div
                  key={att.id}
                  className="group relative flex items-center gap-1.5 rounded-lg border bg-background px-2 py-1.5 text-xs"
                >
                  {att.preview ? (
                    <img src={att.preview} alt={att.file.name} className="h-8 w-8 rounded object-cover" />
                  ) : (
                    <HugeiconsIcon icon={File02Icon} className="h-4 w-4 text-muted-foreground" />
                  )}
                  <span className="max-w-[120px] truncate">{att.file.name}</span>
                  <button
                    type="button"
                    onClick={() => removeAttachment(att.id)}
                    className="ml-0.5 rounded-full p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground cursor-pointer"
                  >
                    <HugeiconsIcon icon={Cancel01Icon} className="h-3 w-3" />
                  </button>
                </div>
              ))}
            </div>
          )}
          <PromptInputTextarea
            placeholder={
              connected ? `Message ${sessionName}...` : "Connecting..."
            }
            autoFocus
            onPaste={handlePaste}
          />
          <div className="flex items-center justify-between px-2">
            <div className="flex items-center gap-2">
              <input
                ref={fileInputRef}
                type="file"
                multiple
                accept="image/*,text/*,application/json,application/xml,application/yaml,application/x-yaml,application/pdf"
                className="hidden"
                onChange={(e) => {
                  if (e.target.files) addAttachments(e.target.files)
                  e.target.value = ""
                }}
              />
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 rounded-full"
                onClick={() => fileInputRef.current?.click()}
                disabled={streaming || !connected}
              >
                <HugeiconsIcon icon={Attachment01Icon} className="h-4 w-4" />
              </Button>
              {currentModelInfo?.can_reason && (
                <ReasoningControl
                  model={currentModelInfo}
                  effort={reasoningEffort}
                  onChange={onReasoningChange}
                />
              )}
            </div>
            <div className="flex items-center gap-2">
              <Button
                size="icon"
                className="h-8 w-8 rounded-full"
                onClick={handleSend}
                disabled={streaming || !connected || (!input.trim() && attachments.length === 0)}
              >
                <HugeiconsIcon icon={ArrowUp01Icon} className="h-4 w-4" />
              </Button>
            </div>
          </div>
        </PromptInput>
      </div>

      {!connected && (
        <p className="mt-2 text-center text-xs text-muted-foreground">
          Connecting to {sessionName}...
        </p>
      )}
    </div>
  )
}
