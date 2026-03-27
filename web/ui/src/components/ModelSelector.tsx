import { useState, useRef, useEffect, useMemo } from "react"
import { ChevronDown } from "lucide-react"
import { cn } from "@/lib/utils"
import { formatTokenCount } from "@/lib/format"
import type { ModelInfo } from "@/lib/chat-types"

export default function ModelSelector({
  models,
  currentModel,
  onSelect,
}: {
  models: ModelInfo[]
  currentModel: string
  onSelect: (id: string) => void
}) {
  const currentName = models.find((m) => m.id === currentModel)?.name || currentModel
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState("")
  const listRef = useRef<HTMLDivElement>(null)
  const filtered = search
    ? models.filter(
        (m) =>
          m.id.toLowerCase().includes(search.toLowerCase()) ||
          m.name.toLowerCase().includes(search.toLowerCase()) ||
          (m.provider ?? "").toLowerCase().includes(search.toLowerCase())
      )
    : models

  // Group by provider when multiple providers are present.
  const providers = useMemo(() => {
    const set = new Set(models.map((m) => m.provider ?? ""))
    return set
  }, [models])
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
    if (!open || search) return
    requestAnimationFrame(() => {
      const el = listRef.current?.querySelector("[data-selected]")
      if (el) el.scrollIntoView({ block: "center" })
    })
  }, [open, search])

  return (
    <div className="relative">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs text-muted-foreground hover:bg-accent cursor-pointer"
      >
        <span>{currentName}</span>
        <ChevronDown className="h-3 w-3" />
      </button>

      {open && (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute left-0 top-full z-50 mt-1 w-80 rounded-lg border bg-popover shadow-md">
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
            <div ref={listRef} className="max-h-80 overflow-y-auto p-1">
              {grouped.map(({ provider, models: groupModels }) => (
                <div key={provider}>
                  {multiProvider && provider && (
                    <div className="sticky top-0 bg-popover px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                      {provider}
                    </div>
                  )}
                  {groupModels.map((m) => (
                    <button
                      key={`${m.provider}:${m.id}`}
                      data-selected={m.id === currentModel ? "" : undefined}
                      onClick={() => {
                        onSelect(m.id)
                        setOpen(false)
                        setSearch("")
                      }}
                      className={cn(
                        "flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left text-xs hover:bg-accent cursor-pointer",
                        m.id === currentModel && "bg-accent"
                      )}
                    >
                      <div className="flex flex-col">
                        <span className="font-medium">{m.name || m.id}</span>
                        {m.name && m.name !== m.id && (
                          <span className="text-[10px] text-muted-foreground">{m.id}</span>
                        )}
                      </div>
                      <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
                        {m.can_reason && <span className="rounded bg-muted px-1">reason</span>}
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
          </div>
        </>
      )}
    </div>
  )
}
