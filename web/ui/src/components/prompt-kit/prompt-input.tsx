import { cn } from "@/lib/utils"
import React, {
  createContext,
  useContext,
  useLayoutEffect,
  useRef,
  useState,
} from "react"

type PromptInputContextType = {
  isLoading: boolean
  value: string
  setValue: (value: string) => void
  maxHeight: number | string
  onSubmit?: () => void
  disabled?: boolean
  textareaRef: React.RefObject<HTMLTextAreaElement | null>
}

const PromptInputContext = createContext<PromptInputContextType>({
  isLoading: false,
  value: "",
  setValue: () => {},
  maxHeight: 240,
  onSubmit: undefined,
  disabled: false,
  textareaRef: React.createRef<HTMLTextAreaElement>(),
})

function usePromptInput() {
  return useContext(PromptInputContext)
}

export type PromptInputProps = {
  isLoading?: boolean
  value?: string
  onValueChange?: (value: string) => void
  maxHeight?: number | string
  onSubmit?: () => void
  children: React.ReactNode
  className?: string
  disabled?: boolean
} & React.ComponentProps<"div">

function PromptInput({
  className,
  isLoading = false,
  maxHeight = 240,
  value,
  onValueChange,
  onSubmit,
  children,
  disabled = false,
  onClick,
  ...props
}: PromptInputProps) {
  const [internalValue, setInternalValue] = useState(value || "")
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const handleChange = (newValue: string) => {
    setInternalValue(newValue)
    onValueChange?.(newValue)
  }

  const handleClick: React.MouseEventHandler<HTMLDivElement> = (e) => {
    if (!disabled) textareaRef.current?.focus()
    onClick?.(e)
  }

  return (
    <PromptInputContext.Provider
      value={{
        isLoading,
        value: value ?? internalValue,
        setValue: onValueChange ?? handleChange,
        maxHeight,
        onSubmit,
        disabled,
        textareaRef,
      }}
    >
      <div
        onClick={handleClick}
        className={cn(
          "cursor-text rounded-2xl border border-input bg-muted/50 p-2",
          disabled && "cursor-not-allowed opacity-60",
          className
        )}
        {...props}
      >
        {children}
      </div>
    </PromptInputContext.Provider>
  )
}

export type PromptInputTextareaProps = {
  disableAutosize?: boolean
} & React.ComponentProps<typeof Textarea>

function PromptInputTextarea({
  className,
  onKeyDown,
  disableAutosize = false,
  ...props
}: PromptInputTextareaProps) {
  const { value, setValue, maxHeight, onSubmit, disabled, textareaRef } =
    usePromptInput()

  const adjustHeight = (el: HTMLTextAreaElement | null) => {
    if (!el || disableAutosize) return

    el.style.height = "auto"

    if (typeof maxHeight === "number") {
      el.style.height = `${Math.min(el.scrollHeight, maxHeight)}px`
    } else {
      el.style.height = `min(${el.scrollHeight}px, ${maxHeight})`
    }
  }

  const handleRef = (el: HTMLTextAreaElement | null) => {
    textareaRef.current = el
    adjustHeight(el)
  }

  useLayoutEffect(() => {
    if (!textareaRef.current || disableAutosize) return

    const el = textareaRef.current
    el.style.height = "auto"

    if (typeof maxHeight === "number") {
      el.style.height = `${Math.min(el.scrollHeight, maxHeight)}px`
    } else {
      el.style.height = `min(${el.scrollHeight}px, ${maxHeight})`
    }
  }, [value, maxHeight, disableAutosize, textareaRef])

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    adjustHeight(e.target)
    setValue(e.target.value)
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault()
      onSubmit?.()
    }
    onKeyDown?.(e)
  }

  return (
    <textarea
      ref={handleRef}
      value={value}
      onChange={handleChange}
      onKeyDown={handleKeyDown}
      className={cn(
        "field-sizing-content min-h-[44px] w-full resize-none border-0 bg-transparent px-2 py-2 text-sm text-foreground outline-none ring-0 placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50",
        className
      )}
      rows={1}
      disabled={disabled}
      {...props}
    />
  )
}

export type PromptInputActionsProps = React.HTMLAttributes<HTMLDivElement>

function PromptInputActions({
  children,
  className,
  ...props
}: PromptInputActionsProps) {
  return (
    <div
      className={cn("flex items-center justify-end gap-2 px-2", className)}
      {...props}
    >
      {children}
    </div>
  )
}

export {
  PromptInput,
  PromptInputTextarea,
  PromptInputActions,
  usePromptInput,
}
