import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { HugeiconsIcon } from "@hugeicons/react"
import { ArrowDown01Icon } from "@hugeicons/core-free-icons"
import { useStickToBottomContext } from "use-stick-to-bottom"

export type ScrollButtonProps = {
  className?: string
} & React.ButtonHTMLAttributes<HTMLButtonElement>

function ScrollButton({ className, ...props }: ScrollButtonProps) {
  const { isAtBottom, scrollToBottom } = useStickToBottomContext()

  return (
    <Button
      variant="outline"
      size="icon"
      className={cn(
        "h-8 w-8 rounded-full transition-all duration-150 ease-out",
        !isAtBottom
          ? "translate-y-0 scale-100 opacity-100"
          : "pointer-events-none translate-y-4 scale-95 opacity-0",
        className
      )}
      onClick={() => scrollToBottom()}
      {...props}
    >
      <HugeiconsIcon icon={ArrowDown01Icon} className="h-4 w-4" />
    </Button>
  )
}

export { ScrollButton }
