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
import {
  ArrowDown01Icon,
  Add01Icon,
  Delete01Icon,
} from "@hugeicons/core-free-icons"
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

interface TelegramConfig {
  bot_token: string
  allowed_chats?: number[]
}

interface SlackConfig {
  bot_token: string
  signing_secret: string
  allowed_channels?: string[]
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
  telegram?: TelegramConfig
  slack?: SlackConfig
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
  const [channels, setChannels] = useState<{ telegram?: TelegramConfig; slack?: SlackConfig }>({})
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
      setChannels({ telegram: data.telegram, slack: data.slack })
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

  // Channel connect/disconnect are immediate — they call the API directly
  // and refresh the config, rather than being part of the deferred Save.
  const connectChannel = useCallback(async (channelData: Record<string, unknown>) => {
    const res = await fetch(`/api/instances/${encodeURIComponent(instanceId)}/config`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(channelData),
    })
    if (!res.ok) {
      const text = await res.text()
      throw new Error(text || `HTTP ${res.status}`)
    }
    await fetchConfig() // refresh to get masked tokens back
    onConfigChanged()
  }, [instanceId, fetchConfig, onConfigChanged])

  const [, currentModelId] = parseModelSpec(model)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl max-h-[85vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>{instanceName}</DialogTitle>
        </DialogHeader>

        {loading ? (
          <div className="flex items-center justify-center py-8 text-sm text-muted-foreground">
            Loading...
          </div>
        ) : (
          <div className="flex flex-col gap-5 overflow-y-auto pr-1 -mr-1">
            {isStopped && (
              <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
                Changes take effect when the instance starts.
              </div>
            )}

            {/* Model & Reasoning */}
            <section className="rounded-lg bg-background p-4">
              <h3 className="mb-3 text-sm font-medium">Model</h3>
              <div className="flex flex-col gap-3">
                <ModelPicker
                  models={models}
                  currentModelId={currentModelId}
                  onSelect={(id) => {
                    const info = models.find((m) => m.id === id)
                    setModel(info?.provider ? formatModelSpec(info.provider, id) : id)
                  }}
                />
                <div className="flex flex-col gap-1.5">
                  <Label className="text-xs text-muted-foreground">Reasoning effort</Label>
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
            </section>

            {/* Persona */}
            <section className="rounded-lg bg-background p-4">
              <h3 className="mb-3 text-sm font-medium">Persona</h3>
              <div className="flex flex-col gap-3">
                <div className="flex gap-3">
                  <div className="flex flex-1 flex-col gap-1.5">
                    <Label className="text-xs text-muted-foreground">Name</Label>
                    <Input
                      value={personaName}
                      onChange={(e) => setPersonaName(e.target.value)}
                      placeholder="Display name"
                    />
                  </div>
                  <div className="flex flex-1 flex-col gap-1.5">
                    <Label className="text-xs text-muted-foreground">Description</Label>
                    <Input
                      value={personaDesc}
                      onChange={(e) => setPersonaDesc(e.target.value)}
                      placeholder="Short description"
                    />
                  </div>
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label className="text-xs text-muted-foreground">Instructions</Label>
                  <Textarea
                    className="text-xs min-h-16"
                    placeholder="Persona instructions for the system prompt..."
                    value={personaBody}
                    onChange={(e) => setPersonaBody(e.target.value)}
                  />
                </div>
              </div>
            </section>

            {/* Tools */}
            <section className="rounded-lg bg-background p-4">
              <h3 className="mb-3 text-sm font-medium">Tools</h3>
              <div className="flex flex-col gap-3">
                <div className="flex flex-col gap-1.5">
                  <Label className="text-xs text-muted-foreground">Allowed</Label>
                  <Textarea
                    className="font-mono text-xs min-h-20"
                    placeholder={"Bash\nRead\nWrite\nBash(curl *)"}
                    value={allowedTools}
                    onChange={(e) => setAllowedTools(e.target.value)}
                  />
                  <p className="text-[11px] text-muted-foreground">One tool per line. Supports patterns like Bash(curl *).</p>
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label className="text-xs text-muted-foreground">Disallowed</Label>
                  <Textarea
                    className="font-mono text-xs min-h-10"
                    placeholder="Bash(rm *)"
                    value={disallowedTools}
                    onChange={(e) => setDisallowedTools(e.target.value)}
                  />
                  <p className="text-[11px] text-muted-foreground">Deny rules override the allowed list above.</p>
                </div>
              </div>
            </section>

            {/* Memory */}
            <section className="rounded-lg bg-background p-4">
              <h3 className="mb-3 text-sm font-medium">Memory</h3>
              <Textarea
                className="text-xs min-h-16"
                placeholder="Agent memories..."
                value={memory}
                onChange={(e) => setMemory(e.target.value)}
              />
              <p className="mt-1.5 text-[11px] text-muted-foreground">Managed by the agent. Manual edits take effect on the next turn.</p>
            </section>

            {/* Channels */}
            <ChannelsSection
              telegram={channels.telegram}
              slack={channels.slack}
              onConnect={connectChannel}
            />
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

// --- Channels section ---

type ChannelType = "telegram" | "slack"

const channelTypes: { id: ChannelType; label: string }[] = [
  { id: "telegram", label: "Telegram" },
  { id: "slack", label: "Slack" },
]

function ChannelsSection({
  telegram,
  slack,
  onConnect,
}: {
  telegram?: TelegramConfig
  slack?: SlackConfig
  onConnect: (data: Record<string, unknown>) => Promise<void>
}) {
  const [adding, setAdding] = useState<ChannelType | null>(null)
  const [connecting, setConnecting] = useState(false)

  // Add form state
  const [tgToken, setTgToken] = useState("")
  const [tgChats, setTgChats] = useState("")
  const [slToken, setSlToken] = useState("")
  const [slSecret, setSlSecret] = useState("")
  const [slChannels, setSlChannels] = useState("")

  const configured = [
    ...(telegram ? [{ type: "telegram" as const, label: "Telegram", maskedToken: telegram.bot_token }] : []),
    ...(slack ? [{ type: "slack" as const, label: "Slack", maskedToken: slack.bot_token }] : []),
  ]
  const available = channelTypes.filter((ct) =>
    !configured.some((c) => c.type === ct.id)
  )

  const resetForm = () => {
    setAdding(null)
    setTgToken("")
    setTgChats("")
    setSlToken("")
    setSlSecret("")
    setSlChannels("")
  }

  const handleConnect = async () => {
    setConnecting(true)
    try {
      if (adding === "telegram") {
        await onConnect({
          telegram: {
            bot_token: tgToken,
            ...(tgChats.trim() && {
              allowed_chats: tgChats.split(",").map((s) => parseInt(s.trim(), 10)).filter((n) => !isNaN(n)),
            }),
          },
        })
      } else if (adding === "slack") {
        await onConnect({
          slack: {
            bot_token: slToken,
            signing_secret: slSecret,
            ...(slChannels.trim() && {
              allowed_channels: slChannels.split(",").map((s) => s.trim()).filter(Boolean),
            }),
          },
        })
      }
      toast.success(`${adding === "telegram" ? "Telegram" : "Slack"} connected`)
      resetForm()
    } catch (err) {
      toast.error(`Failed to connect: ${err instanceof Error ? err.message : "unknown error"}`)
    } finally {
      setConnecting(false)
    }
  }

  const handleDisconnect = async (type: ChannelType) => {
    try {
      await onConnect({ [type]: { bot_token: "" } })
      toast.success(`${type === "telegram" ? "Telegram" : "Slack"} disconnected`)
    } catch (err) {
      toast.error(`Failed to disconnect: ${err instanceof Error ? err.message : "unknown error"}`)
    }
  }

  const canConnect = adding === "telegram" ? tgToken.trim() !== ""
    : adding === "slack" ? slToken.trim() !== "" && slSecret.trim() !== ""
    : false

  return (
    <section className="rounded-lg bg-background p-4">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-medium">Channels</h3>
        {available.length > 0 && !adding && (
          <Popover>
            <PopoverTrigger
              render={<Button variant="outline" size="sm" className="h-7 gap-1 text-xs" />}
            >
              <HugeiconsIcon icon={Add01Icon} className="h-3.5 w-3.5" />
              Add
            </PopoverTrigger>
            <PopoverContent align="end" className="w-40 p-1">
              {available.map((ct) => (
                <button
                  key={ct.id}
                  onClick={() => setAdding(ct.id)}
                  className="flex w-full rounded-md px-2 py-1.5 text-xs hover:bg-accent cursor-pointer"
                >
                  {ct.label}
                </button>
              ))}
            </PopoverContent>
          </Popover>
        )}
      </div>

      <div className="flex flex-col gap-3">
        {/* Configured channels */}
        {configured.map((ch) => (
          <div key={ch.type} className="flex items-center justify-between rounded-md border border-foreground/10 px-3 py-2.5">
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">{ch.label}</span>
              <span className="font-mono text-xs text-muted-foreground">{ch.maskedToken}</span>
            </div>
            <Button
              variant="ghost"
              size="icon-sm"
              className="text-destructive"
              onClick={() => handleDisconnect(ch.type)}
            >
              <HugeiconsIcon icon={Delete01Icon} className="h-4 w-4" />
            </Button>
          </div>
        ))}

        {/* Empty state */}
        {configured.length === 0 && !adding && (
          <p className="text-xs text-muted-foreground py-1">No channels connected.</p>
        )}

        {/* Add form */}
        {adding === "telegram" && (
          <div className="flex flex-col gap-3 rounded-md border border-foreground/10 p-3">
            <div className="flex items-center justify-between">
              <Label className="text-sm font-medium">Telegram</Label>
              <button onClick={resetForm} className="text-xs text-muted-foreground hover:text-foreground cursor-pointer">Cancel</button>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-[11px] text-muted-foreground">Bot token</Label>
              <Input
                className="font-mono text-xs"
                type="password"
                value={tgToken}
                onChange={(e) => setTgToken(e.target.value)}
                placeholder="Paste your bot token from BotFather"
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-[11px] text-muted-foreground">Allowed chat IDs <span className="text-muted-foreground/60">(optional)</span></Label>
              <Input
                className="font-mono text-xs"
                value={tgChats}
                onChange={(e) => setTgChats(e.target.value)}
                placeholder="12345, 67890"
              />
            </div>
            <Button size="sm" onClick={handleConnect} disabled={!canConnect || connecting}>
              {connecting ? "Connecting..." : "Connect"}
            </Button>
          </div>
        )}

        {adding === "slack" && (
          <div className="flex flex-col gap-3 rounded-md border border-foreground/10 p-3">
            <div className="flex items-center justify-between">
              <Label className="text-sm font-medium">Slack</Label>
              <button onClick={resetForm} className="text-xs text-muted-foreground hover:text-foreground cursor-pointer">Cancel</button>
            </div>
            <div className="flex gap-3">
              <div className="flex flex-1 flex-col gap-1.5">
                <Label className="text-[11px] text-muted-foreground">Bot token</Label>
                <Input
                  className="font-mono text-xs"
                  type="password"
                  value={slToken}
                  onChange={(e) => setSlToken(e.target.value)}
                  placeholder="xoxb-..."
                />
              </div>
              <div className="flex flex-1 flex-col gap-1.5">
                <Label className="text-[11px] text-muted-foreground">Signing secret</Label>
                <Input
                  className="font-mono text-xs"
                  type="password"
                  value={slSecret}
                  onChange={(e) => setSlSecret(e.target.value)}
                  placeholder="Signing secret"
                />
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-[11px] text-muted-foreground">Allowed channels <span className="text-muted-foreground/60">(optional)</span></Label>
              <Input
                className="font-mono text-xs"
                value={slChannels}
                onChange={(e) => setSlChannels(e.target.value)}
                placeholder="C123, C456"
              />
            </div>
            <Button size="sm" onClick={handleConnect} disabled={!canConnect || connecting}>
              {connecting ? "Connecting..." : "Connect"}
            </Button>
          </div>
        )}
      </div>
    </section>
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
