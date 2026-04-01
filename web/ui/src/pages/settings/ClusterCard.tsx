import { useState, useEffect, useCallback } from "react"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { HugeiconsIcon } from "@hugeicons/react"
import { Copy01Icon, Tick02Icon } from "@hugeicons/core-free-icons"

interface NodeInfo {
  id: string
  name: string
  status: string
  is_home: boolean
  via?: string
}

interface PendingNode {
  node_id: string
  name: string
  addr: string
  first_seen: string
  last_seen: string
}

interface ClusterSettings {
  mode: string
  node_name?: string
  swarm_code?: string
  tracker_url?: string
  pending_count?: number
  approved_nodes?: Record<string, { name: string; approved_at: string }>
  nodes?: NodeInfo[]
  leader_addr?: string
}

export default function ClusterCard() {
  const [settings, setSettings] = useState<ClusterSettings | null>(null)
  const [pendingNodes, setPendingNodes] = useState<PendingNode[]>([])
  const [copiedField, setCopiedField] = useState("")
  const [actionInProgress, setActionInProgress] = useState("")

  const fetchAll = useCallback(async () => {
    try {
      const [settingsRes, pendingRes] = await Promise.all([
        fetch("/api/settings/cluster"),
        fetch("/api/cluster/pending"),
      ])
      if (settingsRes.ok) setSettings(await settingsRes.json())
      if (pendingRes.ok) setPendingNodes(await pendingRes.json())
    } catch {
      /* ignore */
    }
  }, [])

  // Poll both cluster settings and pending nodes every 5 seconds.
  useEffect(() => {
    fetchAll()
    const interval = setInterval(fetchAll, 5000)
    return () => clearInterval(interval)
  }, [fetchAll])

  const copyToClipboard = (value: string, field: string) => {
    navigator.clipboard.writeText(value)
    setCopiedField(field)
    setTimeout(() => setCopiedField(""), 2000)
  }

  const approveNode = async (nodeID: string) => {
    setActionInProgress(nodeID)
    try {
      await fetch(`/api/cluster/pending/${nodeID}/approve`, { method: "POST" })
      await fetchAll()
    } catch {
      /* ignore */
    } finally {
      setActionInProgress("")
    }
  }

  const dismissNode = async (nodeID: string) => {
    setActionInProgress(nodeID)
    try {
      await fetch(`/api/cluster/pending/${nodeID}`, { method: "DELETE" })
      await fetchAll()
    } catch {
      /* ignore */
    } finally {
      setActionInProgress("")
    }
  }

  const revokeNode = async (nodeID: string) => {
    setActionInProgress(nodeID)
    try {
      await fetch(`/api/cluster/approved/${nodeID}`, { method: "DELETE" })
      await fetchAll()
    } catch {
      /* ignore */
    } finally {
      setActionInProgress("")
    }
  }

  if (!settings) return null

  const modeLabel =
    settings.mode === "leader"
      ? "Leader"
      : settings.mode === "worker"
        ? "Worker"
        : "Standalone"

  const modeDesc =
    settings.mode === "leader"
      ? "Accepting worker connections for distributed execution."
      : settings.mode === "worker"
        ? "Connected to a leader for task execution."
        : "Running independently with no remote connections."

  // Build a unified node list: connected workers + approved-but-offline nodes.
  const connectedNodes = settings.nodes?.filter((n) => !n.is_home) ?? []
  const connectedIDs = new Set(connectedNodes.map((n) => n.id))
  const approvedEntries = Object.entries(settings.approved_nodes ?? {})

  // Offline approved nodes = approved but not in the connected list.
  const offlineApproved = approvedEntries
    .filter(([id]) => !connectedIDs.has(id))
    .map(([id, n]) => ({ id, name: n.name, status: "offline" as const, via: undefined }))

  const allNodes = [
    ...connectedNodes.map((n) => ({ id: n.id, name: n.name, status: n.status, via: n.via })),
    ...offlineApproved,
  ]

  const timeAgo = (dateStr: string) => {
    const diff = Date.now() - new Date(dateStr).getTime()
    const mins = Math.floor(diff / 60000)
    if (mins < 1) return "just now"
    if (mins < 60) return `${mins}m ago`
    const hours = Math.floor(mins / 60)
    if (hours < 24) return `${hours}h ago`
    return `${Math.floor(hours / 24)}d ago`
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle>Cluster</CardTitle>
          <div className="flex items-center gap-2">
            {pendingNodes.length > 0 && (
              <Badge variant="destructive" className="text-xs">
                {pendingNodes.length} pending
              </Badge>
            )}
            <Badge variant="outline">{modeLabel}</Badge>
          </div>
        </div>
        <CardDescription>{modeDesc}</CardDescription>
      </CardHeader>
      <CardContent>
        {settings.mode === "leader" && (
          <div className="flex flex-col gap-4">
            {settings.swarm_code && (
              <div className="flex flex-col gap-1">
                <span className="text-sm font-medium">Swarm Code</span>
                <div className="flex items-center gap-2">
                  <code className="rounded bg-muted px-2 py-1 text-sm font-mono">
                    {settings.swarm_code}
                  </code>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 w-7 p-0"
                    onClick={() =>
                      copyToClipboard(settings.swarm_code!, "swarm")
                    }
                  >
                    <HugeiconsIcon
                      icon={
                        copiedField === "swarm" ? Tick02Icon : Copy01Icon
                      }
                      className="h-3.5 w-3.5"
                    />
                  </Button>
                </div>
              </div>
            )}

            {/* Pending Nodes */}
            {pendingNodes.length > 0 && (
              <div className="flex flex-col gap-2">
                <span className="text-sm font-medium">Pending Approval</span>
                <div className="flex flex-col gap-2">
                  {pendingNodes.map((node) => (
                    <div
                      key={node.node_id}
                      className="flex items-center justify-between rounded-lg border p-3"
                    >
                      <div className="flex flex-col gap-0.5">
                        <span className="text-sm font-medium">
                          {node.name || "unnamed"}
                        </span>
                        <span className="text-xs text-muted-foreground">
                          <span className="font-mono">
                            {node.node_id.slice(0, 12)}...
                          </span>
                          {" "}&middot; {node.addr} &middot;{" "}
                          {timeAgo(node.first_seen)}
                        </span>
                      </div>
                      <div className="flex items-center gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          className="h-8 border-green-600 text-green-600 hover:bg-green-600 hover:text-white"
                          onClick={() => approveNode(node.node_id)}
                          disabled={actionInProgress === node.node_id}
                        >
                          Approve
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          className="h-8"
                          onClick={() => dismissNode(node.node_id)}
                          disabled={actionInProgress === node.node_id}
                        >
                          Dismiss
                        </Button>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Unified Nodes List */}
            <div className="flex flex-col gap-2">
              <span className="text-sm font-medium">
                Nodes ({allNodes.length})
              </span>
              {allNodes.length === 0 ? (
                <span className="text-sm text-muted-foreground">
                  No workers approved yet.
                </span>
              ) : (
                <div className="flex flex-col gap-2">
                  {allNodes.map((node) => (
                    <div
                      key={node.id}
                      className="flex items-center gap-2 text-sm"
                    >
                      <div
                        className={`h-2 w-2 shrink-0 rounded-full ${
                          node.status === "online"
                            ? "bg-green-500"
                            : "bg-muted-foreground"
                        }`}
                      />
                      <span className="flex-1 truncate">{node.name}</span>
                      {node.via && (
                        <Badge
                          variant="outline"
                          className={`text-[10px] px-1.5 py-0 h-5 ${
                            node.via === "direct"
                              ? "border-green-500 text-green-500"
                              : "border-destructive text-destructive"
                          }`}
                        >
                          {node.via}
                        </Badge>
                      )}
                      <span className="text-xs text-muted-foreground font-mono shrink-0">
                        {node.id.slice(0, 8)}...
                      </span>
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-7 px-2 text-xs shrink-0"
                        onClick={() => revokeNode(node.id)}
                        disabled={actionInProgress === node.id}
                      >
                        Revoke
                      </Button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        )}

        {settings.mode === "worker" && (
          <div className="flex flex-col gap-2">
            {settings.leader_addr && (
              <div className="flex items-center justify-between text-sm">
                <span className="text-muted-foreground">Leader</span>
                <code className="font-mono">{settings.leader_addr}</code>
              </div>
            )}
            {settings.node_name && (
              <div className="flex items-center justify-between text-sm">
                <span className="text-muted-foreground">Node Name</span>
                <span>{settings.node_name}</span>
              </div>
            )}
          </div>
        )}

        {settings.mode === "standalone" && (
          <p className="text-sm text-muted-foreground">
            Running in standalone mode — no remote cluster connections.
          </p>
        )}
      </CardContent>
    </Card>
  )
}
