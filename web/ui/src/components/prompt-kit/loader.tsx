import { cn } from "@/lib/utils"

export interface LoaderProps {
  variant?: "typing" | "text-shimmer" | "loading-dots"
  size?: "sm" | "md" | "lg"
  text?: string
  className?: string
}

function TypingLoader({
  className,
  size = "md",
}: {
  className?: string
  size?: "sm" | "md" | "lg"
}) {
  const dotSizes = {
    sm: "h-1 w-1",
    md: "h-1.5 w-1.5",
    lg: "h-2 w-2",
  }

  const containerSizes = {
    sm: "h-4",
    md: "h-5",
    lg: "h-6",
  }

  return (
    <div
      className={cn(
        "flex items-center gap-1",
        containerSizes[size],
        className
      )}
    >
      {[...Array(3)].map((_, i) => (
        <div
          key={i}
          className={cn(
            "animate-[typing_1s_infinite] rounded-full bg-muted-foreground/50",
            dotSizes[size]
          )}
          style={{
            animationDelay: `${i * 250}ms`,
          }}
        />
      ))}
      <span className="sr-only">Loading</span>
    </div>
  )
}

function TextShimmerLoader({
  text = "Thinking",
  className,
  size = "md",
}: {
  text?: string
  className?: string
  size?: "sm" | "md" | "lg"
}) {
  const textSizes = {
    sm: "text-xs",
    md: "text-sm",
    lg: "text-base",
  }

  return (
    <div
      className={cn(
        "bg-[linear-gradient(to_right,var(--muted-foreground)_40%,var(--foreground)_60%,var(--muted-foreground)_80%)]",
        "bg-size-[200%_auto] bg-clip-text font-medium text-transparent",
        "animate-[shimmer_4s_infinite_linear]",
        textSizes[size],
        className
      )}
    >
      {text}
    </div>
  )
}

function TextDotsLoader({
  className,
  text = "Thinking",
  size = "md",
}: {
  className?: string
  text?: string
  size?: "sm" | "md" | "lg"
}) {
  const textSizes = {
    sm: "text-xs",
    md: "text-sm",
    lg: "text-base",
  }

  return (
    <div className={cn("inline-flex items-center", className)}>
      <span
        className={cn("font-medium text-muted-foreground", textSizes[size])}
      >
        {text}
      </span>
      <span className="inline-flex">
        <span className="animate-[loading-dots_1.4s_infinite_0.2s] text-muted-foreground">
          .
        </span>
        <span className="animate-[loading-dots_1.4s_infinite_0.4s] text-muted-foreground">
          .
        </span>
        <span className="animate-[loading-dots_1.4s_infinite_0.6s] text-muted-foreground">
          .
        </span>
      </span>
    </div>
  )
}

function Loader({ variant = "typing", size = "md", text, className }: LoaderProps) {
  switch (variant) {
    case "typing":
      return <TypingLoader size={size} className={className} />
    case "text-shimmer":
      return <TextShimmerLoader text={text} size={size} className={className} />
    case "loading-dots":
      return <TextDotsLoader text={text} size={size} className={className} />
    default:
      return <TypingLoader size={size} className={className} />
  }
}

export { Loader }
