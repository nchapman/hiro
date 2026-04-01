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
import {
  Loading02Icon,
  CheckmarkCircle01Icon,
  CancelCircleIcon,
  Link01Icon,
} from "@hugeicons/core-free-icons"

interface ClusterSettings {
  mode: string
  node_name?: string
  leader_addr?: string
  swarm_code?: string
  connection_status?: string // "connecting" | "pending" | "connected" | "disconnected"
}

interface WorkerStatusProps {
  onDisconnect: () => void
}

export default function WorkerStatus({ onDisconnect }: WorkerStatusProps) {
  const [settings, setSettings] = useState<ClusterSettings | null>(null)
  const [connStatus, setConnStatus] = useState<string>("loading")
  const [disconnecting, setDisconnecting] = useState(false)

  const fetchStatus = useCallback(async () => {
    try {
      const res = await fetch("/api/settings/cluster")
      if (res.ok) {
        const data: ClusterSettings = await res.json()
        setSettings(data)
        setConnStatus(data.connection_status || "connecting")
      }
    } catch {
      setConnStatus("disconnected")
    }
  }, [])

  useEffect(() => {
    fetchStatus()
    const interval = setInterval(fetchStatus, 5000)
    return () => clearInterval(interval)
  }, [fetchStatus])

  const handleDisconnect = async () => {
    if (
      !confirm(
        "This will remove all cluster configuration and restart. Continue?"
      )
    )
      return

    setDisconnecting(true)
    try {
      const res = await fetch("/api/settings/cluster/reset", { method: "POST" })
      if (!res.ok) {
        setDisconnecting(false)
        return
      }
      // Server will restart — poll until it comes back in setup mode.
      setTimeout(onDisconnect, 3000)
    } catch {
      setDisconnecting(false)
    }
  }

  const statusIcon =
    connStatus === "connected" ? CheckmarkCircle01Icon :
    connStatus === "pending" ? Loading02Icon :
    connStatus === "disconnected" ? CancelCircleIcon :
    Loading02Icon

  const statusLabel =
    connStatus === "connected"
      ? "Connected"
      : connStatus === "pending"
        ? "Waiting for Approval"
        : connStatus === "disconnected"
          ? "Disconnected"
          : "Connecting..."

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="mx-auto mb-2 flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <HugeiconsIcon
              icon={Link01Icon}
              className="h-6 w-6 text-muted-foreground"
            />
          </div>
          <CardTitle>Worker Node</CardTitle>
          <CardDescription>
            {settings?.node_name || "This machine"} is running as a worker
            node.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex flex-col gap-4">
            {/* Connection status */}
            <div className="flex items-center justify-between rounded-lg border p-3">
              <span className="text-sm font-medium">Status</span>
              <Badge
                variant="outline"
                className={`gap-1 ${
                  connStatus === "connected"
                    ? "border-green-500 text-green-500"
                    : connStatus === "pending"
                      ? "border-yellow-500 text-yellow-500"
                      : connStatus === "disconnected"
                        ? "border-destructive text-destructive"
                        : ""
                }`}
              >
                <HugeiconsIcon icon={statusIcon} className="h-3 w-3" />
                {statusLabel}
              </Badge>
            </div>

            {/* Details */}
            <div className="flex flex-col gap-2 rounded-lg border p-3">
              {settings?.node_name && (
                <div className="flex items-center justify-between text-sm">
                  <span className="text-muted-foreground">Node Name</span>
                  <span className="font-medium">{settings.node_name}</span>
                </div>
              )}
              {settings?.leader_addr && (
                <div className="flex items-center justify-between text-sm">
                  <span className="text-muted-foreground">Leader</span>
                  <code className="font-mono text-xs">
                    {settings.leader_addr}
                  </code>
                </div>
              )}
              {settings?.swarm_code && (
                <div className="flex items-center justify-between text-sm">
                  <span className="text-muted-foreground">Swarm</span>
                  <code className="font-mono text-xs">
                    {settings.swarm_code}
                  </code>
                </div>
              )}
            </div>

            <p className="text-center text-xs text-muted-foreground">
              {connStatus === "pending"
                ? "The leader operator needs to approve this machine from their dashboard before it can connect."
                : "This node receives tasks from the leader and executes them locally. Manage agents and settings from the leader dashboard."}
            </p>

            <Button
              variant="outline"
              className="w-full text-destructive hover:text-destructive"
              onClick={handleDisconnect}
              disabled={disconnecting}
            >
              {disconnecting && (
                <HugeiconsIcon
                  icon={Loading02Icon}
                  className="mr-2 h-4 w-4 animate-spin"
                />
              )}
              Disconnect from Cluster
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
