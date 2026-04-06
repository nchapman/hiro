import { useState, useEffect, useCallback, useMemo } from "react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
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
  persona_name: string
  persona_description: string
  persona_body: string
  memory: string
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
  const [personaName, setPersonaName] = useState("")
  const [personaDesc, setPersonaDesc] = useState("")
  const [personaBody, setPersonaBody] = useState("")
  const [memory, setMemory] = useState("")
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
      setPersonaName(data.persona_name)
      setPersonaDesc(data.persona_description)
      setPersonaBody(data.persona_body)
      setMemory(data.memory)
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
    if (personaName !== original.persona_name) return true
    if (personaDesc !== original.persona_description) return true
    if (personaBody !== original.persona_body) return true
    if (memory !== original.memory) return true
    return false
  }, [model, reasoningEffort, allowedTools, disallowedTools, personaName, personaDesc, personaBody, memory, original])

  const handleSave = useCallback(async () => {
    setSaving(true)
    try {
      const body: Record<string, unknown> = {}
      if (model !== original?.model) body.model = model
      if (reasoningEffort !== original?.reasoning_effort) body.reasoning_effort = reasoningEffort
      const toolsChanged = allowedTools !== original?.allowed_tools.join("\n")
        || disallowedTools !== original?.disallowed_tools.join("\n")
      if (toolsChanged) {
        body.allowed_tools = allowedTools.split("\n").map((s) => s.trim()).filter(Boolean)
        body.disallowed_tools = disallowedTools.split("\n").map((s) => s.trim()).filter(Boolean)
      }
      if (personaName !== original?.persona_name) body.persona_name = personaName
      if (personaDesc !== original?.persona_description) body.persona_description = personaDesc
      if (personaBody !== original?.persona_body) body.persona_body = personaBody
      if (memory !== original?.memory) body.memory = memory

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
  }, [instanceId, model, reasoningEffort, allowedTools, disallowedTools, personaName, personaDesc, personaBody, memory, original, onConfigChanged, onOpenChange])

  // Extract current model ID for matching against the model list.
  const [, currentModelId] = parseModelSpec(model)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg max-h-[85vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>{instanceName}</DialogTitle>
        </DialogHeader>

        {loading ? (
          <div className="flex items-center justify-center py-8 text-sm text-muted-foreground">
            Loading...
          </div>
        ) : (
          <div className="flex flex-col gap-4 overflow-y-auto pr-1">
            {isStopped && (
              <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
                Changes take effect when the instance starts.
              </div>
            )}

            {/* Model & Reasoning */}
            <div className="flex flex-col gap-3">
              <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Model</Label>
              <div className="flex flex-col gap-1.5">
                <ModelPicker
                  models={models}
                  currentModelId={currentModelId}
                  onSelect={(id) => {
                    const info = models.find((m) => m.id === id)
                    setModel(info?.provider ? formatModelSpec(info.provider, id) : id)
                  }}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label className="text-xs">Reasoning effort</Label>
                <Select value={reasoningEffort} onValueChange={(v) => { if (v !== null) setReasoningEffort(v) }}>
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
            </div>

            {/* Persona */}
            <div className="flex flex-col gap-3">
              <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Persona</Label>
              <div className="flex gap-2">
                <div className="flex flex-1 flex-col gap-1">
                  <Label className="text-xs">Name</Label>
                  <Input
                    value={personaName}
                    onChange={(e) => setPersonaName(e.target.value)}
                    placeholder="Display name"
                  />
                </div>
                <div className="flex flex-1 flex-col gap-1">
                  <Label className="text-xs">Description</Label>
                  <Input
                    value={personaDesc}
                    onChange={(e) => setPersonaDesc(e.target.value)}
                    placeholder="Short description"
                  />
                </div>
              </div>
              <div className="flex flex-col gap-1">
                <Label className="text-xs">Instructions</Label>
                <Textarea
                  className="text-xs min-h-16"
                  placeholder="Persona instructions for the system prompt..."
                  value={personaBody}
                  onChange={(e) => setPersonaBody(e.target.value)}
                />
              </div>
            </div>

            {/* Tools */}
            <div className="flex flex-col gap-3">
              <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Tools</Label>
              <div className="flex flex-col gap-1.5">
                <Label className="text-xs">Allowed</Label>
                <Textarea
                  className="font-mono text-xs min-h-20"
                  placeholder={"Bash\nRead\nWrite\nBash(curl *)"}
                  value={allowedTools}
                  onChange={(e) => setAllowedTools(e.target.value)}
                />
                <p className="text-[11px] text-muted-foreground">One tool per line. Supports patterns like Bash(curl *).</p>
              </div>
              <div className="flex flex-col gap-1.5">
                <Label className="text-xs">Disallowed</Label>
                <Textarea
                  className="font-mono text-xs min-h-10"
                  placeholder="Bash(rm *)"
                  value={disallowedTools}
                  onChange={(e) => setDisallowedTools(e.target.value)}
                />
                <p className="text-[11px] text-muted-foreground">Deny rules override the allowed list above.</p>
              </div>
            </div>

            {/* Memory */}
            <div className="flex flex-col gap-3">
              <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Memory</Label>
              <Textarea
                className="text-xs min-h-16"
                placeholder="Agent memories..."
                value={memory}
                onChange={(e) => setMemory(e.target.value)}
              />
              <p className="text-[11px] text-muted-foreground">Managed by the agent. Manual edits take effect on the next turn.</p>
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
