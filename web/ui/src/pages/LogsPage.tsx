import { useState, useEffect, useCallback, useRef } from "react"
import {
  Search,
  Pause,
  Play,
  Trash2,
  ArrowDown,
  ChevronRight,
  ChevronDown,
} from "lucide-react"
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
import { cn } from "@/lib/utils"
import { useLogStream, type LogEntry } from "@/hooks/use-log-stream"

const MAX_ENTRIES = 2000
const PAGE_SIZE = 200

const levelColors: Record<string, string> = {
  DEBUG: "bg-muted text-muted-foreground",
  INFO: "bg-blue-500/15 text-blue-700 dark:text-blue-400",
  WARN: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  ERROR: "bg-destructive/15 text-destructive",
}

function formatTime(iso: string): string {
  try {
    const d = new Date(iso)
    return d.toLocaleTimeString("en-US", {
      hour12: false,
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      fractionalSecondDigits: 3,
    })
  } catch {
    return iso
  }
}

/** Build query string with active filters. */
function buildQuery(params: {
  limit: number
  before?: number
  level?: string
  component?: string
  search?: string
}): string {
  const q = new URLSearchParams()
  q.set("limit", String(params.limit))
  if (params.before) q.set("before", String(params.before))
  if (params.level && params.level !== "all") q.set("level", params.level)
  if (params.component && params.component !== "all")
    q.set("component", params.component)
  if (params.search) q.set("search", params.search)
  return q.toString()
}

export default function LogsPage() {
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [level, setLevel] = useState<string>("all")
  const [component, setComponent] = useState<string>("all")
  const [search, setSearch] = useState("")
  const [debouncedSearch, setDebouncedSearch] = useState("")
  const [sources, setSources] = useState<string[]>([])
  const [paused, setPaused] = useState(false)
  const [expandedIds, setExpandedIds] = useState<Set<number>>(new Set())
  const [autoScroll, setAutoScroll] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [hasMore, setHasMore] = useState(true)
  const scrollRef = useRef<HTMLDivElement>(null)
  const oldestIdRef = useRef<number | null>(null)

  // Debounce search input (300ms).
  useEffect(() => {
    const timer = setTimeout(() => setDebouncedSearch(search), 300)
    return () => clearTimeout(timer)
  }, [search])

  // Fetch sources on mount.
  useEffect(() => {
    fetch("/api/logs/sources")
      .then((r) => r.json())
      .then((data: string[]) => setSources(data))
      .catch(() => {})
  }, [])

  // Fetch logs whenever filters change.
  useEffect(() => {
    const qs = buildQuery({
      limit: PAGE_SIZE,
      level,
      component,
      search: debouncedSearch,
    })
    fetch(`/api/logs?${qs}`)
      .then((r) => r.json())
      .then((data: LogEntry[]) => {
        const reversed = data.reverse()
        setLogs(reversed)
        setHasMore(reversed.length >= PAGE_SIZE)
        oldestIdRef.current =
          reversed.length > 0 ? reversed[0].id : null
      })
      .catch(() => {})
  }, [level, component, debouncedSearch])

  // Real-time log stream — client-side filtering for SSE entries.
  useLogStream(
    useCallback(
      (entry: LogEntry) => {
        setLogs((prev) => {
          const next = [...prev, entry]
          return next.length > MAX_ENTRIES
            ? next.slice(next.length - MAX_ENTRIES)
            : next
        })
      },
      [],
    ),
    !paused,
  )

  // Auto-scroll to bottom when new logs arrive.
  useEffect(() => {
    if (autoScroll && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [logs, autoScroll])

  // Detect scroll position to toggle auto-scroll.
  const handleScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    setAutoScroll(atBottom)
  }, [])

  // Load older logs (cursor pagination with filters).
  const loadMore = useCallback(() => {
    if (loadingMore || !hasMore || oldestIdRef.current === null) return
    setLoadingMore(true)
    const qs = buildQuery({
      limit: PAGE_SIZE,
      before: oldestIdRef.current,
      level,
      component,
      search: debouncedSearch,
    })
    fetch(`/api/logs?${qs}`)
      .then((r) => r.json())
      .then((data: LogEntry[]) => {
        const reversed = data.reverse()
        setLogs((prev) => [...reversed, ...prev])
        setHasMore(reversed.length >= PAGE_SIZE)
        if (reversed.length > 0) {
          oldestIdRef.current = reversed[0].id
        }
      })
      .catch(() => {})
      .finally(() => setLoadingMore(false))
  }, [loadingMore, hasMore, level, component, debouncedSearch])

  const toggleExpand = (id: number) => {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  // Client-side filtering for SSE entries (server filters historical fetches).
  const filtered = logs.filter((e) => {
    if (level !== "all" && e.level !== level) return false
    if (component !== "all" && (e.component ?? "") !== component) return false
    if (
      debouncedSearch &&
      !e.message.toLowerCase().includes(debouncedSearch.toLowerCase())
    )
      return false
    return true
  })

  return (
    <div className="flex h-full flex-1 flex-col overflow-hidden">
      {/* Toolbar */}
      <div className="flex items-center gap-2 border-b px-4 py-2">
        <Select value={level} onValueChange={(v) => v && setLevel(v)}>
          <SelectTrigger size="sm">
            <SelectValue placeholder="Level" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All Levels</SelectItem>
            <SelectItem value="DEBUG">Debug</SelectItem>
            <SelectItem value="INFO">Info</SelectItem>
            <SelectItem value="WARN">Warn</SelectItem>
            <SelectItem value="ERROR">Error</SelectItem>
          </SelectContent>
        </Select>

        <Select value={component} onValueChange={(v) => v && setComponent(v)}>
          <SelectTrigger size="sm">
            <SelectValue placeholder="Component" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All Components</SelectItem>
            {sources.map((s) => (
              <SelectItem key={s} value={s}>
                {s}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <div className="relative">
          <Search className="absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder="Search logs..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-7 w-48 pl-7 text-xs"
          />
        </div>

        <div className="flex-1" />

        <Tooltip>
          <TooltipTrigger
            onClick={() => setPaused((p) => !p)}
            className="inline-flex h-7 w-7 items-center justify-center rounded-md cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
          >
            {paused ? (
              <Play className="h-3.5 w-3.5" />
            ) : (
              <Pause className="h-3.5 w-3.5" />
            )}
          </TooltipTrigger>
          <TooltipContent>{paused ? "Resume" : "Pause"}</TooltipContent>
        </Tooltip>

        <Tooltip>
          <TooltipTrigger
            onClick={() => {
              setLogs([])
              setExpandedIds(new Set())
              oldestIdRef.current = null
            }}
            className="inline-flex h-7 w-7 items-center justify-center rounded-md cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </TooltipTrigger>
          <TooltipContent>Clear</TooltipContent>
        </Tooltip>
      </div>

      {/* Log list */}
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="flex-1 overflow-y-auto font-mono text-[13px] leading-relaxed"
      >
        {hasMore && filtered.length > 0 && (
          <button
            onClick={loadMore}
            disabled={loadingMore}
            className="w-full py-1.5 text-center text-xs text-muted-foreground hover:bg-muted/50 transition-colors disabled:opacity-50"
          >
            {loadingMore ? "Loading..." : "Load older logs"}
          </button>
        )}

        {filtered.length === 0 ? (
          <div className="flex h-full items-center justify-center text-muted-foreground text-sm">
            {logs.length === 0 ? "No logs yet" : "No matching logs"}
          </div>
        ) : (
          filtered.map((entry) => {
            const expanded = expandedIds.has(entry.id)
            const hasAttrs =
              entry.attrs && Object.keys(entry.attrs).length > 0
            return (
              <div key={entry.id} className="border-b border-border/40">
                <div
                  onClick={() => hasAttrs && toggleExpand(entry.id)}
                  className={cn(
                    "flex items-start gap-2 px-4 py-1 hover:bg-muted/30 transition-colors",
                    hasAttrs && "cursor-pointer",
                    entry.level === "ERROR" && "bg-destructive/5",
                    entry.level === "WARN" && "bg-amber-500/5",
                  )}
                >
                  {/* Expand indicator */}
                  <span className="mt-0.5 w-3.5 shrink-0 text-muted-foreground">
                    {hasAttrs &&
                      (expanded ? (
                        <ChevronDown className="h-3.5 w-3.5" />
                      ) : (
                        <ChevronRight className="h-3.5 w-3.5" />
                      ))}
                  </span>

                  {/* Timestamp */}
                  <span className="shrink-0 text-muted-foreground">
                    {formatTime(entry.time)}
                  </span>

                  {/* Level badge */}
                  <span
                    className={cn(
                      "shrink-0 rounded px-1.5 py-0 text-[11px] font-medium",
                      levelColors[entry.level] ?? levelColors.INFO,
                    )}
                  >
                    {entry.level.padEnd(5)}
                  </span>

                  {/* Component */}
                  {entry.component && (
                    <Badge
                      variant="outline"
                      className="shrink-0 text-[11px] h-[18px] px-1.5 rounded"
                    >
                      {entry.component}
                    </Badge>
                  )}

                  {/* Message */}
                  <span className="min-w-0 flex-1 break-words">
                    {entry.message}
                  </span>
                </div>

                {/* Expanded attrs */}
                {expanded && entry.attrs && (
                  <div className="ml-14 border-l-2 border-muted px-4 py-1.5 text-xs text-muted-foreground">
                    {Object.entries(entry.attrs).map(([k, v]) => (
                      <div key={k} className="flex gap-2">
                        <span className="shrink-0 font-medium text-foreground/70">
                          {k}:
                        </span>
                        <span className="break-all">
                          {typeof v === "object"
                            ? JSON.stringify(v)
                            : String(v)}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )
          })
        )}
      </div>

      {/* Jump to bottom */}
      {!autoScroll && (
        <button
          onClick={() => {
            setAutoScroll(true)
            if (scrollRef.current) {
              scrollRef.current.scrollTop = scrollRef.current.scrollHeight
            }
          }}
          className="absolute bottom-4 right-6 z-10 flex items-center gap-1 rounded-full bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground shadow-lg transition-opacity hover:bg-primary/90"
        >
          <ArrowDown className="h-3 w-3" />
          Jump to bottom
        </button>
      )}
    </div>
  )
}
