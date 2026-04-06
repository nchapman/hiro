import { useState, useEffect, useCallback, useMemo } from "react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Badge } from "@/components/ui/badge"
import { HugeiconsIcon } from "@hugeicons/react"
import { ArrowDown01Icon } from "@hugeicons/core-free-icons"
import { formatTokenCount } from "@/lib/format"
import { parseModelSpec, formatModelSpec } from "@/pages/settings/DefaultModelCard"
import type { ModelInfo } from "@/lib/chat-types"

interface InstanceConfigModalProps {
  instanceId: string
  instanceName: string
  isStopped: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
  models: ModelInfo[]
  onConfigChanged: () => void
}

interface InstanceConfig {
  model: string
  reasoning_effort: string
  allowed_tools: string[]
  disallowed_tools: string[]
}

const reasoningOptions = [
  { value: "", label: "Default" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "max", label: "Max" },
]

export default function InstanceConfigModal({
  instanceId,
  instanceName,
  isStopped,
  open,
  onOpenChange,
  models,
  onConfigChanged,
}: InstanceConfigModalProps) {
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [model, setModel] = useState("")
  const [reasoningEffort, setReasoningEffort] = useState("")
  const [allowedTools, setAllowedTools] = useState("")
  const [disallowedTools, setDisallowedTools] = useState("")
  const [original, setOriginal] = useState<InstanceConfig | null>(null)

  const fetchConfig = useCallback(async () => {
    setLoading(true)
    try {
      const res = await fetch(`/api/instances/${encodeURIComponent(instanceId)}/config`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data: InstanceConfig = await res.json()
      setModel(data.model)
      setReasoningEffort(data.reasoning_effort)
      setAllowedTools(data.allowed_tools.join("\n"))
      setDisallowedTools(data.disallowed_tools.join("\n"))
      setOriginal(data)
    } catch {
      toast.error("Failed to load instance config")
    } finally {
      setLoading(false)
    }
  }, [instanceId])

  useEffect(() => {
    if (open) fetchConfig()
  }, [open, fetchConfig])

  const hasChanges = useMemo(() => {
    if (!original) return false
    if (model !== original.model) return true
    if (reasoningEffort !== original.reasoning_effort) return true
    if (allowedTools !== original.allowed_tools.join("\n")) return true
    if (disallowedTools !== original.disallowed_tools.join("\n")) return true
    return false
  }, [model, reasoningEffort, allowedTools, disallowedTools, original])

  const handleSave = useCallback(async () => {
    setSaving(true)
    try {
      const body: Record<string, unknown> = {}
      if (model !== original?.model) body.model = model
      if (reasoningEffort !== original?.reasoning_effort) body.reasoning_effort = reasoningEffort
      const newAllowed = allowedTools.split("\n").map((s) => s.trim()).filter(Boolean)
      const newDisallowed = disallowedTools.split("\n").map((s) => s.trim()).filter(Boolean)
      if (allowedTools !== original?.allowed_tools.join("\n")) {
        body.allowed_tools = newAllowed
        body.disallowed_tools = newDisallowed
      }

      const res = await fetch(`/api/instances/${encodeURIComponent(instanceId)}/config`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }
      toast.success("Configuration updated")
      onConfigChanged()
      onOpenChange(false)
    } catch (err) {
      toast.error(`Failed to save: ${err instanceof Error ? err.message : "unknown error"}`)
    } finally {
      setSaving(false)
    }
  }, [instanceId, model, reasoningEffort, allowedTools, disallowedTools, original, onConfigChanged, onOpenChange])

  // Extract current model ID for matching against the model list.
  const [, currentModelId] = parseModelSpec(model)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{instanceName}</DialogTitle>
        </DialogHeader>

        {loading ? (
          <div className="flex items-center justify-center py-8 text-sm text-muted-foreground">
            Loading...
          </div>
        ) : (
          <div className="flex flex-col gap-4">
            {isStopped && (
              <p className="text-xs text-muted-foreground">
                Changes take effect when the instance starts.
              </p>
            )}

            {/* Model */}
            <div className="flex flex-col gap-1.5">
              <Label>Model</Label>
              <ModelPicker
                models={models}
                currentModelId={currentModelId}
                onSelect={(id) => {
                  const info = models.find((m) => m.id === id)
                  setModel(info?.provider ? formatModelSpec(info.provider, id) : id)
                }}
              />
            </div>

            {/* Reasoning Effort */}
            <div className="flex flex-col gap-1.5">
              <Label>Reasoning effort</Label>
              <Select value={reasoningEffort} onValueChange={setReasoningEffort}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {reasoningOptions.map((o) => (
                    <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {/* Allowed Tools */}
            <div className="flex flex-col gap-1.5">
              <Label>Allowed tools</Label>
              <Textarea
                className="font-mono text-xs min-h-20"
                placeholder={"Bash\nRead\nWrite\nBash(curl *)"}
                value={allowedTools}
                onChange={(e) => setAllowedTools(e.target.value)}
              />
            </div>

            {/* Disallowed Tools */}
            <div className="flex flex-col gap-1.5">
              <Label>Disallowed tools</Label>
              <Textarea
                className="font-mono text-xs min-h-10"
                placeholder="Bash(rm *)"
                value={disallowedTools}
                onChange={(e) => setDisallowedTools(e.target.value)}
              />
            </div>
          </div>
        )}

        <DialogFooter>
          <Button
            onClick={handleSave}
            disabled={saving || loading || !hasChanges}
          >
            {saving ? "Saving..." : "Save"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// --- Inline model picker ---

function ModelPicker({
  models,
  currentModelId,
  onSelect,
}: {
  models: ModelInfo[]
  currentModelId: string
  onSelect: (id: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState("")
  const currentName = models.find((m) => m.id === currentModelId)?.name || currentModelId || "Select model"

  const filtered = search
    ? models.filter(
        (m) =>
          m.id.toLowerCase().includes(search.toLowerCase()) ||
          m.name.toLowerCase().includes(search.toLowerCase()) ||
          (m.provider ?? "").toLowerCase().includes(search.toLowerCase())
      )
    : models

  const providers = useMemo(() => new Set(models.map((m) => m.provider ?? "")), [models])
  const multiProvider = providers.size > 1

  const grouped = useMemo(() => {
    if (!multiProvider) return [{ provider: "", models: filtered }]
    const map = new Map<string, ModelInfo[]>()
    for (const m of filtered) {
      const p = m.provider ?? ""
      if (!map.has(p)) map.set(p, [])
      map.get(p)!.push(m)
    }
    return Array.from(map.entries())
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([provider, models]) => ({ provider, models }))
  }, [filtered, multiProvider])

  useEffect(() => {
    if (!open) setSearch("")
  }, [open])

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button variant="outline" className="w-full justify-between h-9 px-3 text-sm font-normal" />
        }
      >
        <span className="truncate">{currentName}</span>
        <HugeiconsIcon icon={ArrowDown01Icon} className="h-3 w-3 shrink-0 opacity-50" />
      </PopoverTrigger>

      <PopoverContent align="start" className="w-80 p-0">
        {models.length > 10 && (
          <div className="border-b p-2">
            <input
              type="text"
              className="w-full rounded-md border bg-transparent px-2 py-1 text-xs outline-none placeholder:text-muted-foreground"
              placeholder="Search models..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              autoFocus
            />
          </div>
        )}
        <div className="max-h-60 overflow-y-auto p-1">
          {grouped.map(({ provider, models: groupModels }) => (
            <div key={provider}>
              {multiProvider && provider && (
                <div className="sticky -top-1 -mx-1 bg-popover px-3 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                  {provider}
                </div>
              )}
              {groupModels.map((m) => (
                <button
                  key={`${m.provider}:${m.id}`}
                  onClick={() => { onSelect(m.id); setOpen(false) }}
                  className={cn(
                    "flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left text-xs hover:bg-accent cursor-pointer",
                    m.id === currentModelId && "bg-accent"
                  )}
                >
                  <div className="flex flex-col">
                    <span className="font-medium">{m.name || m.id}</span>
                    {m.name && m.name !== m.id && (
                      <span className="text-[10px] text-muted-foreground">{m.id}</span>
                    )}
                  </div>
                  <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
                    {m.can_reason && <Badge variant="secondary">reason</Badge>}
                    <span>{formatTokenCount(m.context_window)}</span>
                  </div>
                </button>
              ))}
            </div>
          ))}
          {filtered.length === 0 && (
            <div className="px-2 py-3 text-center text-xs text-muted-foreground">
              No models found
            </div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}
