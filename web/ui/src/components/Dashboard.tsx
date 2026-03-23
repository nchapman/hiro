import { useState, useEffect } from "react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { ScrollArea } from "@/components/ui/scroll-area"

interface WorkerInfo {
  id: string
  agent_name: string
  description: string
  skills: string[]
  connected_at: string
}

interface SwarmStatus {
  worker_count: number
  active_tasks: number
}

export default function Dashboard() {
  const [status, setStatus] = useState<SwarmStatus | null>(null)
  const [workers, setWorkers] = useState<WorkerInfo[]>([])

  useEffect(() => {
    const fetchData = async () => {
      try {
        const [statusRes, workersRes] = await Promise.all([
          fetch("/api/swarm"),
          fetch("/api/workers"),
        ])
        setStatus(await statusRes.json())
        setWorkers(await workersRes.json())
      } catch {
        /* API not available */
      }
    }

    fetchData()
    const interval = setInterval(fetchData, 5000)
    return () => clearInterval(interval)
  }, [])

  return (
    <ScrollArea className="flex-1">
      <div className="p-6">
        <h1 className="mb-6 text-xl font-bold">Swarm Dashboard</h1>

        <div className="mb-8 grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-4">
          <Card>
            <CardHeader className="pb-2">
              <CardTitle className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Workers
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="text-3xl font-bold">
                {status?.worker_count ?? "\u2014"}
              </div>
            </CardContent>
          </Card>
          <Card>
            <CardHeader className="pb-2">
              <CardTitle className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Active Tasks
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="text-3xl font-bold">
                {status?.active_tasks ?? "\u2014"}
              </div>
            </CardContent>
          </Card>
        </div>

        <div>
          <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Connected Workers
          </h2>
          {workers.length === 0 ? (
            <p className="text-sm italic text-muted-foreground">
              No workers connected
            </p>
          ) : (
            <div className="flex flex-col gap-2">
              {workers.map((w) => (
                <Card key={w.id}>
                  <CardContent className="flex items-center justify-between py-4">
                    <div>
                      <div className="flex items-center gap-2 text-sm font-semibold">
                        <span className="h-2 w-2 rounded-full bg-green-500" />
                        {w.agent_name}
                      </div>
                      {w.description && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          {w.description}
                        </p>
                      )}
                    </div>
                    <div className="flex gap-1.5">
                      {w.skills?.map((skill) => (
                        <Badge key={skill} variant="secondary">
                          {skill}
                        </Badge>
                      ))}
                    </div>
                  </CardContent>
                </Card>
              ))}
            </div>
          )}
        </div>
      </div>
    </ScrollArea>
  )
}
