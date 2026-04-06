import { useState, useEffect, useCallback, useMemo } from "react"
import { HugeiconsIcon } from "@hugeicons/react"
import {
  Search01Icon,
  Delete01Icon,
  PauseIcon,
  PlayIcon,
  Alert02Icon,
} from "@hugeicons/core-free-icons"
import { Badge } from "@/components/ui/badge"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Input } from "@/components/ui/input"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"

interface SubscriptionInfo {
  id: string
  instance_id: string
  instance_name: string
  name: string
  trigger_type: string
  schedule: string
  message: string
  status: string
  next_fire: string | null
  last_fired: string | null
  fire_count: number
  error_count: number
  last_error: string
  created_at: string
}

function formatRelativeTime(iso: string): string {
  const target = new Date(iso)
  const now = new Date()
  const diffMs = target.getTime() - now.getTime()

  if (diffMs < 0) {
    const ago = -diffMs
    if (ago < 60_000) return "just now"
    if (ago < 3_600_000) return `${Math.floor(ago / 60_000)}m ago`
    if (ago < 86_400_000) return `${Math.floor(ago / 3_600_000)}h ago`
    return `${Math.floor(ago / 86_400_000)}d ago`
  }

  if (diffMs < 60_000) return "in <1m"
  if (diffMs < 3_600_000) return `in ${Math.floor(diffMs / 60_000)}m`
  if (diffMs < 86_400_000) {
    const h = Math.floor(diffMs / 3_600_000)
    const m = Math.floor((diffMs % 3_600_000) / 60_000)
    return m > 0 ? `in ${h}h ${m}m` : `in ${h}h`
  }
  return `in ${Math.floor(diffMs / 86_400_000)}d`
}

function formatAbsoluteTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString("en-US", {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    })
  } catch {
    return iso
  }
}

export default function SchedulesPage() {
  const [subs, setSubs] = useState<SubscriptionInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [instanceFilter, setInstanceFilter] = useState("all")
  const [typeFilter, setTypeFilter] = useState("all")
  const [search, setSearch] = useState("")
  const [debouncedSearch, setDebouncedSearch] = useState("")
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set())

  useEffect(() => {
    const timer = setTimeout(() => setDebouncedSearch(search), 300)
    return () => clearTimeout(timer)
  }, [search])

  const fetchSubs = useCallback(async () => {
    try {
      const res = await fetch("/api/subscriptions")
      if (res.ok) {
        setSubs(await res.json())
      }
    } catch {
      /* ignore */
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchSubs()
    const interval = setInterval(fetchSubs, 10_000)
    return () => clearInterval(interval)
  }, [fetchSubs])

  const instances = useMemo(() => {
    const map = new Map<string, string>()
    for (const s of subs) {
      if (!map.has(s.instance_id)) {
        map.set(s.instance_id, s.instance_name || s.instance_id.slice(0, 8))
      }
    }
    return Array.from(map.entries()).map(([id, name]) => ({ id, name }))
  }, [subs])

  const filtered = useMemo(() => {
    return subs.filter((s) => {
      if (instanceFilter !== "all" && s.instance_id !== instanceFilter) return false
      if (typeFilter !== "all" && s.trigger_type !== typeFilter) return false
      if (debouncedSearch && !s.name.toLowerCase().includes(debouncedSearch.toLowerCase())) return false
      return true
    })
  }, [subs, instanceFilter, typeFilter, debouncedSearch])

  const grouped = useMemo(() => {
    const map = new Map<string, { label: string; items: SubscriptionInfo[] }>()
    for (const s of filtered) {
      if (!map.has(s.instance_id)) {
        map.set(s.instance_id, {
          label: s.instance_name || s.instance_id.slice(0, 8),
          items: [],
        })
      }
      map.get(s.instance_id)!.items.push(s)
    }
    return Array.from(map.values())
  }, [filtered])

  const handleDelete = async (id: string, name: string) => {
    if (!window.confirm(`Delete schedule "${name}"?`)) return
    try {
      const res = await fetch(`/api/subscriptions/${id}`, { method: "DELETE" })
      if (res.ok) {
        setSubs((prev) => prev.filter((s) => s.id !== id))
      }
    } catch {
      /* ignore */
    }
  }

  const handlePause = async (id: string) => {
    try {
      const res = await fetch(`/api/subscriptions/${id}/pause`, { method: "POST" })
      if (res.ok) {
        setSubs((prev) => prev.map((s) => s.id === id ? { ...s, status: "paused", next_fire: null } : s))
      }
    } catch {
      /* ignore */
    }
  }

  const handleResume = async (id: string) => {
    try {
      const res = await fetch(`/api/subscriptions/${id}/resume`, { method: "POST" })
      if (res.ok) {
        setSubs((prev) => prev.map((s) => s.id === id ? { ...s, status: "active" } : s))
      }
    } catch {
      /* ignore */
    }
  }

  const toggleExpand = (id: string) => {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <div className="flex h-full flex-1 flex-col overflow-hidden">
      {/* Toolbar */}
      <div className="flex h-12 items-center gap-2 border-b px-4">
        <span className="font-heading text-sm font-medium mr-2">Schedules</span>

        <Select value={instanceFilter} onValueChange={(v) => v && setInstanceFilter(v)}>
          <SelectTrigger size="sm">
            <SelectValue placeholder="Instance" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All Instances</SelectItem>
            {instances.map((inst) => (
              <SelectItem key={inst.id} value={inst.id}>
                {inst.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select value={typeFilter} onValueChange={(v) => v && setTypeFilter(v)}>
          <SelectTrigger size="sm">
            <SelectValue placeholder="Type" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All Types</SelectItem>
            <SelectItem value="cron">Cron</SelectItem>
            <SelectItem value="once">Once</SelectItem>
          </SelectContent>
        </Select>

        <div className="relative">
          <HugeiconsIcon icon={Search01Icon} className="absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder="Search schedules..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-7 w-48 pl-7 text-xs"
          />
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto">
        {loading ? (
          <div className="mx-auto flex max-w-3xl flex-col gap-6 p-6">
            {Array.from({ length: 2 }).map((_, i) => (
              <div key={i} className="flex flex-col gap-3 rounded-xl border p-4">
                <Skeleton className="h-4 w-24 rounded" />
                <div className="flex flex-col gap-2">
                  {Array.from({ length: 2 }).map((_, j) => (
                    <div key={j} className="flex items-center gap-3 rounded-lg border border-border/50 p-3">
                      <Skeleton className="h-2 w-2 rounded-full" />
                      <Skeleton className="h-4 w-32 rounded" />
                      <Skeleton className="h-4 w-16 rounded" />
                      <Skeleton className="h-4 w-24 rounded" />
                      <div className="flex-1" />
                      <Skeleton className="h-4 w-20 rounded" />
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </div>
        ) : filtered.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center gap-2 text-muted-foreground">
            <p className="text-sm">
              {subs.length === 0
                ? "No active schedules"
                : "No matching schedules"}
            </p>
            {subs.length === 0 && (
              <p className="text-xs">
                Agents can create schedules using the ScheduleRecurring and ScheduleOnce tools.
              </p>
            )}
          </div>
        ) : (
          <div className="mx-auto flex max-w-3xl flex-col gap-6 p-6">
            {grouped.map((group) => (
              <div key={group.label} className="flex flex-col gap-2">
                <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground px-1">
                  {group.label}
                </span>
                <div className="flex flex-col gap-1.5">
                  {group.items.map((sub) => (
                    <ScheduleCard
                      key={sub.id}
                      sub={sub}
                      expanded={expandedIds.has(sub.id)}
                      onToggle={() => toggleExpand(sub.id)}
                      onPause={() => handlePause(sub.id)}
                      onResume={() => handleResume(sub.id)}
                      onDelete={() => handleDelete(sub.id, sub.name)}
                    />
                  ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

interface ScheduleCardProps {
  sub: SubscriptionInfo
  expanded: boolean
  onToggle: () => void
  onPause: () => void
  onResume: () => void
  onDelete: () => void
}

function ScheduleCard({ sub, expanded, onToggle, onPause, onResume, onDelete }: ScheduleCardProps) {
  const isActive = sub.status === "active"
  const hasError = sub.error_count > 0

  return (
    <div className="rounded-lg border border-border/50 transition-colors hover:border-border">
      <div
        className="flex items-center gap-3 px-3 py-2.5 cursor-pointer"
        onClick={onToggle}
      >
        {/* Status dot */}
        <span
          className={cn(
            "h-2 w-2 shrink-0 rounded-full",
            isActive ? "bg-emerald-500" : "bg-muted-foreground/40",
          )}
        />

        {/* Name */}
        <span className="font-medium text-sm min-w-0 truncate">{sub.name}</span>

        {/* Type badge */}
        <Badge variant="outline" className="shrink-0 text-[11px] h-[18px] px-1.5 rounded">
          {sub.trigger_type}
        </Badge>

        {/* Schedule expression */}
        <code className="shrink-0 text-xs text-muted-foreground font-mono">
          {sub.schedule}
        </code>

        <div className="flex-1" />

        {/* Next fire / status */}
        {isActive && sub.next_fire ? (
          <Tooltip>
            <TooltipTrigger>
              <span className="shrink-0 text-xs text-muted-foreground">
                {formatRelativeTime(sub.next_fire)}
              </span>
            </TooltipTrigger>
            <TooltipContent>{formatAbsoluteTime(sub.next_fire)}</TooltipContent>
          </Tooltip>
        ) : (
          <span className="shrink-0 text-xs text-muted-foreground">paused</span>
        )}

        {/* Fire count */}
        <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
          {sub.fire_count} {sub.fire_count === 1 ? "fire" : "fires"}
        </span>

        {/* Error indicator */}
        {hasError && (
          <Tooltip>
            <TooltipTrigger>
              <span className="shrink-0 flex items-center gap-0.5 text-xs text-destructive">
                <HugeiconsIcon icon={Alert02Icon} className="h-3 w-3" />
                {sub.error_count}
              </span>
            </TooltipTrigger>
            <TooltipContent className="max-w-xs">
              <p className="font-medium">Last error:</p>
              <p className="text-xs break-words">{sub.last_error || "Unknown error"}</p>
            </TooltipContent>
          </Tooltip>
        )}

        {/* Actions */}
        <div className="flex items-center gap-0.5" onClick={(e) => e.stopPropagation()}>
          {isActive ? (
            <Tooltip>
              <TooltipTrigger
                onClick={onPause}
                className="inline-flex h-6 w-6 items-center justify-center rounded cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
              >
                <HugeiconsIcon icon={PauseIcon} className="h-3 w-3" />
              </TooltipTrigger>
              <TooltipContent>Pause</TooltipContent>
            </Tooltip>
          ) : (
            <Tooltip>
              <TooltipTrigger
                onClick={onResume}
                className="inline-flex h-6 w-6 items-center justify-center rounded cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
              >
                <HugeiconsIcon icon={PlayIcon} className="h-3 w-3" />
              </TooltipTrigger>
              <TooltipContent>Resume</TooltipContent>
            </Tooltip>
          )}
          <Tooltip>
            <TooltipTrigger
              onClick={onDelete}
              className="inline-flex h-6 w-6 items-center justify-center rounded cursor-pointer transition-colors text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            >
              <HugeiconsIcon icon={Delete01Icon} className="h-3 w-3" />
            </TooltipTrigger>
            <TooltipContent>Delete</TooltipContent>
          </Tooltip>
        </div>
      </div>

      {/* Expanded details */}
      {expanded && (
        <div className="border-t border-border/50 px-3 py-2.5 text-xs space-y-1.5">
          <div className="flex gap-2">
            <span className="text-muted-foreground w-20 shrink-0">Message</span>
            <span className="break-words">{sub.message}</span>
          </div>
          {sub.last_fired && (
            <div className="flex gap-2">
              <span className="text-muted-foreground w-20 shrink-0">Last fired</span>
              <span>{formatAbsoluteTime(sub.last_fired)} ({formatRelativeTime(sub.last_fired)})</span>
            </div>
          )}
          {sub.last_error && (
            <div className="flex gap-2">
              <span className="text-muted-foreground w-20 shrink-0">Last error</span>
              <span className="text-destructive break-words">{sub.last_error}</span>
            </div>
          )}
          <div className="flex gap-2">
            <span className="text-muted-foreground w-20 shrink-0">Created</span>
            <span>{formatAbsoluteTime(sub.created_at)}</span>
          </div>
        </div>
      )}
    </div>
  )
}
