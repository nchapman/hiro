import { cn } from "@/lib/utils"
import { useRef, useImperativeHandle, forwardRef } from "react"
import { StickToBottom, type StickToBottomContext } from "use-stick-to-bottom"

export type ChatContainerRootProps = {
  children: React.ReactNode
  className?: string
} & React.HTMLAttributes<HTMLDivElement>

export interface ChatContainerHandle {
  /** The underlying scroll element managed by StickToBottom. */
  scrollElement: HTMLElement | null
}

export type ChatContainerContentProps = {
  children: React.ReactNode
  className?: string
} & React.HTMLAttributes<HTMLDivElement>

export type ChatContainerScrollAnchorProps = {
  className?: string
} & React.HTMLAttributes<HTMLDivElement>

const ChatContainerRoot = forwardRef<ChatContainerHandle, ChatContainerRootProps>(
  function ChatContainerRoot({ children, className, ...props }, ref) {
    const ctxRef = useRef<StickToBottomContext>(null)

    useImperativeHandle(ref, () => ({
      get scrollElement() {
        return ctxRef.current?.scrollRef.current ?? null
      },
    }))

    return (
      <StickToBottom
        className={cn("flex overflow-y-auto", className)}
        resize="smooth"
        initial="instant"
        role="log"
        contextRef={ctxRef}
        {...props}
      >
        {children}
      </StickToBottom>
    )
  }
)

function ChatContainerContent({
  children,
  className,
  ...props
}: ChatContainerContentProps) {
  return (
    <StickToBottom.Content
      className={cn("flex w-full flex-col", className)}
      {...props}
    >
      {children}
    </StickToBottom.Content>
  )
}

function ChatContainerScrollAnchor({
  className,
  ...props
}: ChatContainerScrollAnchorProps) {
  return (
    <div
      className={cn("h-px w-full shrink-0 scroll-mt-4", className)}
      aria-hidden="true"
      {...props}
    />
  )
}

export { ChatContainerRoot, ChatContainerContent, ChatContainerScrollAnchor }
