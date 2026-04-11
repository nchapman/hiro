import { memo } from "react"
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@/components/ui/collapsible"
import { HugeiconsIcon } from "@hugeicons/react"
import { Wrench01Icon, ArrowRight01Icon, File02Icon, UserMultiple02Icon } from "@hugeicons/core-free-icons"
import { cn } from "@/lib/utils"
import { isImageType } from "@/lib/file-utils"
import { Markdown } from "@/components/prompt-kit/markdown"
import { Loader } from "@/components/prompt-kit/loader"
import type { ToolCall, AgentNotification, Message } from "@/lib/chat-types"

// --- Formatting helpers ---

function formatJSON(input: string): string {
  try {
    return JSON.stringify(JSON.parse(input), null, 2)
  } catch {
    return input
  }
}

function formatAsCodeBlock(output: string): string {
  try {
    const parsed = JSON.parse(output)
    return "```json\n" + JSON.stringify(parsed, null, 2) + "\n```"
  } catch {
    return "```\n" + output + "\n```"
  }
}

// --- Shared styles ---

const markdownClassName = cn(
  "prose prose-sm dark:prose-invert max-w-none",
  "prose-pre:my-2 prose-code:before:content-none prose-code:after:content-none"
)

// --- Tool call UI ---

export const ToolCallBlock = memo(function ToolCallBlock({ toolCall }: { toolCall: ToolCall }) {
  const hasDetails = toolCall.input || toolCall.output

  return (
    <Collapsible className="rounded-lg border">
      <CollapsibleTrigger
        disabled={!hasDetails}
        className={cn(
          "flex w-full items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors",
          hasDetails && "cursor-pointer hover:bg-muted/50"
        )}
      >
        <HugeiconsIcon icon={Wrench01Icon} className="h-3 w-3 shrink-0" />
        {toolCall.status || toolCall.name}
        <span className="flex-1" />
        {hasDetails && (
          <HugeiconsIcon icon={ArrowRight01Icon} className="h-3 w-3 shrink-0 transition-transform [[data-panel-open]_&]:rotate-90" />
        )}
      </CollapsibleTrigger>

      {hasDetails && (
        <CollapsibleContent>
          {toolCall.input && (
            <div className="border-t bg-muted/20 px-3 py-2 text-xs">
              <Markdown className={markdownClassName}>
                {"```json\n" + formatJSON(toolCall.input) + "\n```"}
              </Markdown>
            </div>
          )}
          {toolCall.output && (
            <div className="border-t bg-muted/20 px-3 py-2 text-xs">
              {toolCall.isError && (
                <div className="mb-1 font-medium text-destructive">Error</div>
              )}
              <Markdown className={markdownClassName}>
                {formatAsCodeBlock(toolCall.output)}
              </Markdown>
            </div>
          )}
        </CollapsibleContent>
      )}
    </Collapsible>
  )
})

// --- Agent notification UI ---

export const NotificationBlock = memo(function NotificationBlock({ notification }: { notification: AgentNotification }) {
  const isFailed = notification.status === "failed"
  const label = isFailed
    ? `Agent "${notification.agent}" failed`
    : `Agent "${notification.agent}" completed`

  return (
    <Collapsible className="rounded-lg border">
      <CollapsibleTrigger
        className="flex w-full items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-muted-foreground cursor-pointer transition-colors hover:bg-muted/50"
      >
        <HugeiconsIcon icon={UserMultiple02Icon} className="h-3 w-3 shrink-0" />
        <span className={cn(isFailed && "text-destructive")}>{label}</span>
        <span className="flex-1" />
        <HugeiconsIcon icon={ArrowRight01Icon} className="h-3 w-3 shrink-0 transition-transform [[data-panel-open]_&]:rotate-90" />
      </CollapsibleTrigger>

      <CollapsibleContent>
        <div className="border-t bg-muted/20 px-3 py-2 text-xs">
          <div className="mb-1 font-medium">{notification.summary}</div>
          {notification.result && (
            <div className="whitespace-pre-wrap break-words text-muted-foreground">{notification.result}</div>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
})

// --- Thinking block ---

export const ThinkingBlock = memo(function ThinkingBlock({ content, isStreaming }: { content: string; isStreaming?: boolean }) {
  return (
    <Collapsible defaultOpen={false} className="rounded-lg border border-dashed border-muted-foreground/30">
      <CollapsibleTrigger className="flex w-full items-center gap-2 px-3 py-1.5 text-xs text-muted-foreground cursor-pointer hover:bg-accent/50 rounded-lg [[data-panel-open]_&]:rounded-b-none">
        {isStreaming ? (
          <Loader variant="typing" size="sm" />
        ) : (
          <HugeiconsIcon icon={ArrowRight01Icon} className="h-3 w-3 transition-transform [[data-panel-open]_&]:rotate-90" />
        )}
        <span>{isStreaming ? "Thinking..." : "Thought process"}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="border-t border-dashed border-muted-foreground/30 px-3 py-2 text-xs text-muted-foreground whitespace-pre-wrap max-h-64 overflow-y-auto">
          {content}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
})

// --- Assistant message ---

export const AssistantMessage = memo(function AssistantMessage({ message }: { message: Message }) {
  const toolCalls = message.toolCalls ?? []
  const notifications = message.notifications ?? []
  const content = message.content

  return (
    <div className="flex flex-col gap-2">
      {message.thinking && (
        <ThinkingBlock content={message.thinking} isStreaming={message.isThinking} />
      )}
      {notifications.length > 0 && (
        <div className="flex flex-col gap-1.5">
          {notifications.map((n, i) => (
            <NotificationBlock key={`notif-${i}`} notification={n} />
          ))}
        </div>
      )}
      {toolCalls.length > 0 && (
        <div className="flex flex-col gap-1.5">
          {toolCalls.map((tc) => (
            <ToolCallBlock key={tc.id} toolCall={tc} />
          ))}
        </div>
      )}
      {content && (
        <Markdown className={markdownClassName}>
          {content}
        </Markdown>
      )}
    </div>
  )
})

// --- User message ---

export const UserMessage = memo(function UserMessage({ message }: { message: Message }) {
  return (
    <div className="flex justify-end">
      <div className="flex max-w-[85%] flex-col gap-2">
        {message.attachments && message.attachments.length > 0 && (
          <div className="flex flex-wrap justify-end gap-2">
            {message.attachments.map((att, i) =>
              isImageType(att.media_type) && att.data ? (
                <img
                  key={i}
                  src={`data:${att.media_type};base64,${att.data}`}
                  alt={att.filename}
                  className="max-h-48 max-w-full rounded-lg object-contain"
                />
              ) : (
                <div key={i} className="flex items-center gap-1.5 rounded-lg bg-muted px-3 py-1.5 text-xs text-muted-foreground">
                  <HugeiconsIcon icon={File02Icon} className="h-3 w-3" />
                  {att.filename}
                </div>
              )
            )}
          </div>
        )}
        {message.content && (
          <div className="rounded-2xl bg-muted px-4 py-2.5 text-sm">
            {message.content}
          </div>
        )}
      </div>
    </div>
  )
})
