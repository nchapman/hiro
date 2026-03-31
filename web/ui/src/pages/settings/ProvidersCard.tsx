import { useState, useCallback } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DialogClose,
} from "@/components/ui/dialog"
import { Tooltip, TooltipTrigger, TooltipContent } from "@/components/ui/tooltip"
import { HugeiconsIcon } from "@hugeicons/react"
import {
  Add01Icon,
  Delete01Icon,
  CheckmarkCircle01Icon,
  CancelCircleIcon,
  Loading02Icon,
} from "@hugeicons/core-free-icons"

export interface ProviderInfo {
  api_key: string
  base_url?: string
}

export interface ProviderTypeInfo {
  id: string
  name: string
}

interface ProvidersCardProps {
  /** Map of provider ID → display name, computed once in the parent. */
  providerItems: Record<string, string>
  providers: Record<string, ProviderInfo>
  onProvidersChanged: () => void
}

export default function ProvidersCard({
  providerItems,
  providers,
  onProvidersChanged,
}: ProvidersCardProps) {
  const [addType, setAddType] = useState("")
  const [addKey, setAddKey] = useState("")
  const [addOpen, setAddOpen] = useState(false)
  const [testStatus, setTestStatus] = useState<
    Record<string, "idle" | "testing" | "success" | "error">
  >({})
  const [testErrors, setTestErrors] = useState<Record<string, string>>({})

  const providerLabel = useCallback(
    (type: string) => providerItems[type] ?? type,
    [providerItems]
  )

  const allTypes = Object.keys(providerItems)
  const availableTypes = allTypes.filter((id) => !providers[id])
  const configuredTypes = Object.keys(providers)

  const handleAddProvider = async () => {
    if (!addType || !addKey) return
    try {
      const res = await fetch(`/api/settings/providers/${encodeURIComponent(addType)}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ api_key: addKey }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      setAddType("")
      setAddKey("")
      setAddOpen(false)
      onProvidersChanged()
    } catch { toast.error("Failed to add provider") }
  }

  const handleDeleteProvider = async (type: string) => {
    try {
      const res = await fetch(`/api/settings/providers/${encodeURIComponent(type)}`, {
        method: "DELETE",
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      onProvidersChanged()
    } catch { toast.error("Failed to delete provider") }
  }

  const handleTestProvider = async (type: string) => {
    setTestStatus((s) => ({ ...s, [type]: "testing" }))
    setTestErrors((s) => ({ ...s, [type]: "" }))
    try {
      const res = await fetch(
        `/api/settings/providers/${encodeURIComponent(type)}/test`,
        { method: "POST" }
      )
      const data = await res.json()
      setTestStatus((s) => ({
        ...s,
        [type]: data.valid ? "success" : "error",
      }))
      if (!data.valid && data.error) {
        setTestErrors((s) => ({ ...s, [type]: data.error }))
      }
    } catch {
      setTestStatus((s) => ({ ...s, [type]: "error" }))
      setTestErrors((s) => ({ ...s, [type]: "Network error" }))
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div>
            <CardTitle>LLM Providers</CardTitle>
            <CardDescription>
              Configure your AI provider API keys.
            </CardDescription>
          </div>
          {availableTypes.length > 0 && (
            <Dialog open={addOpen} onOpenChange={setAddOpen}>
              <DialogTrigger>
                <Button size="sm" className="gap-1">
                  <HugeiconsIcon icon={Add01Icon} className="h-4 w-4" />
                  Add
                </Button>
              </DialogTrigger>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>Add Provider</DialogTitle>
                  <DialogDescription>
                    Select a provider and enter your API key.
                  </DialogDescription>
                </DialogHeader>
                <div className="flex flex-col gap-4 pt-2">
                  <div className="flex flex-col gap-2">
                    <Label>Provider</Label>
                    <Select
                      value={addType}
                      onValueChange={(v) => {
                        if (v) setAddType(v)
                      }}
                      items={providerItems}
                    >
                      <SelectTrigger>
                        <SelectValue placeholder="Select provider..." />
                      </SelectTrigger>
                      <SelectContent>
                        {availableTypes.map((id) => (
                          <SelectItem key={id} value={id}>
                            {providerLabel(id)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="flex flex-col gap-2">
                    <Label>API Key</Label>
                    <Input
                      type="password"
                      value={addKey}
                      onChange={(e) => setAddKey(e.target.value)}
                      placeholder="sk-..."
                    />
                  </div>
                  <div className="flex justify-end gap-2">
                    <DialogClose>
                      <Button variant="outline">Cancel</Button>
                    </DialogClose>
                    <Button
                      onClick={handleAddProvider}
                      disabled={!addType || !addKey}
                    >
                      Add Provider
                    </Button>
                  </div>
                </div>
              </DialogContent>
            </Dialog>
          )}
        </div>
      </CardHeader>
      <CardContent>
        {configuredTypes.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No providers configured.
          </p>
        ) : (
          <div className="flex flex-col gap-4">
            {configuredTypes.map((type_) => (
              <div key={type_} className="flex flex-col gap-2">
                <div className="flex items-center justify-between">
                  <Label className="text-sm font-semibold">
                    {providerLabel(type_)}
                  </Label>
                  <div className="flex items-center gap-1">
                    {testStatus[type_] === "success" && (
                      <HugeiconsIcon icon={CheckmarkCircle01Icon} className="h-4 w-4 text-green-500" />
                    )}
                    {testStatus[type_] === "error" && (
                      <HugeiconsIcon icon={CancelCircleIcon} className="h-4 w-4 text-destructive" />
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => handleTestProvider(type_)}
                      disabled={testStatus[type_] === "testing"}
                    >
                      {testStatus[type_] === "testing" ? (
                        <HugeiconsIcon icon={Loading02Icon} className="h-4 w-4 animate-spin" />
                      ) : (
                        "Test"
                      )}
                    </Button>
                    <Tooltip>
                      <TooltipTrigger
                        render={
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8 text-destructive"
                            onClick={() => handleDeleteProvider(type_)}
                            disabled={configuredTypes.length <= 1}
                          >
                            <HugeiconsIcon icon={Delete01Icon} className="h-4 w-4" />
                          </Button>
                        }
                      />
                      <TooltipContent>
                        {configuredTypes.length <= 1
                          ? "At least one provider is required"
                          : "Remove provider"}
                      </TooltipContent>
                    </Tooltip>
                  </div>
                </div>
                <Input
                  value={providers[type_]?.api_key ?? ""}
                  readOnly
                  className="font-mono text-xs"
                />
                {testErrors[type_] && (
                  <p className="text-xs text-destructive">
                    {testErrors[type_]}
                  </p>
                )}
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
