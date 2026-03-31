import { useState, useEffect } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
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

import type { ModelInfo } from "@/lib/chat-types"

export interface Settings {
  default_provider: string
  default_model: string
}

interface DefaultModelCardProps {
  /** Map of provider ID → display name, computed once in the parent. */
  providerItems: Record<string, string>
  configuredProviders: string[]
  settings: Settings
  savedSettings: Settings
  onSettingsChange: (settings: Settings) => void
  onSaved: (settings: Settings) => void
}

export default function DefaultModelCard({
  providerItems,
  configuredProviders,
  settings,
  savedSettings,
  onSettingsChange,
  onSaved,
}: DefaultModelCardProps) {
  const [defaultModels, setDefaultModels] = useState<ModelInfo[]>([])

  const providerLabel = (type: string) => providerItems[type] ?? type

  // Fetch models when the selected default provider changes.
  useEffect(() => {
    if (!settings.default_provider) {
      setDefaultModels([])
      return
    }
    fetch(`/api/models?provider=${encodeURIComponent(settings.default_provider)}`)
      .then((res) => (res.ok ? res.json() : []))
      .then((data: ModelInfo[]) => setDefaultModels(data ?? []))
      .catch(() => setDefaultModels([]))
  }, [settings.default_provider])

  const settingsChanged =
    settings.default_provider !== savedSettings.default_provider ||
    settings.default_model !== savedSettings.default_model

  const handleSave = async () => {
    try {
      const res = await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(settings),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      onSaved(settings)
    } catch { toast.error("Failed to save settings") }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Default Model</CardTitle>
        <CardDescription>
          Used when agents don't specify their own model and provider.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex gap-2">
          <div className="flex flex-col gap-2">
            <Label>Provider</Label>
            <Select
              value={settings.default_provider}
              onValueChange={(v) => {
                if (v)
                  onSettingsChange({
                    ...settings,
                    default_provider: v,
                    default_model: "",
                  })
              }}
              items={providerItems}
            >
              <SelectTrigger className="w-40">
                <SelectValue placeholder="Select..." />
              </SelectTrigger>
              <SelectContent>
                {configuredProviders.map((type_) => (
                  <SelectItem key={type_} value={type_}>
                    {providerLabel(type_)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-1 flex-col gap-2">
            <Label>Model</Label>
            <Select
              value={settings.default_model}
              onValueChange={(v) => {
                if (v)
                  onSettingsChange({ ...settings, default_model: v })
              }}
              items={Object.fromEntries(
                defaultModels.map((m) => [m.id, m.name || m.id])
              )}
            >
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Select model..." />
              </SelectTrigger>
              <SelectContent>
                {defaultModels.map((m) => (
                  <SelectItem key={m.id} value={m.id}>
                    {m.name || m.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
        <Button
          onClick={handleSave}
          disabled={!settingsChanged}
          variant="outline"
          className="w-fit"
        >
          Save
        </Button>
      </CardContent>
    </Card>
  )
}
