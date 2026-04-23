import { useState, useEffect, useCallback, useMemo } from "react"
import {
  Card,
  CardContent,
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
import { useTheme } from "@/hooks/use-theme"

import ProvidersCard from "@/pages/settings/ProvidersCard"
import type { ProviderInfo, ProviderTypeInfo } from "@/pages/settings/ProvidersCard"
import DefaultModelCard from "@/pages/settings/DefaultModelCard"
import type { Settings } from "@/pages/settings/DefaultModelCard"
import SecurityCard from "@/pages/settings/SecurityCard"
import ClusterCard from "@/pages/settings/ClusterCard"
import GitIdentityCard from "@/pages/settings/GitIdentityCard"

export default function SettingsPage() {
  const { themeId, setThemeId, availableThemes } = useTheme()
  const [providerTypes, setProviderTypes] = useState<ProviderTypeInfo[]>([])
  const [providers, setProviders] = useState<Record<string, ProviderInfo>>({})
  const [settings, setSettings] = useState<Settings>({
    default_model: "",
  })
  const [savedSettings, setSavedSettings] = useState<Settings>({
    default_model: "",
  })

  // Derived once, shared by ProvidersCard and DefaultModelCard.
  const providerItems = useMemo(
    () => Object.fromEntries(providerTypes.map((p) => [p.id, p.name])),
    [providerTypes]
  )

  const fetchProviderTypes = useCallback(async () => {
    try {
      const res = await fetch("/api/provider-types")
      if (res.ok) setProviderTypes(await res.json())
    } catch {
      /* ignore */
    }
  }, [])

  const fetchProviders = useCallback(async () => {
    try {
      const res = await fetch("/api/settings/providers")
      if (res.ok) setProviders(await res.json())
    } catch {
      /* ignore */
    }
  }, [])

  const fetchSettings = useCallback(async () => {
    try {
      const res = await fetch("/api/settings")
      if (res.ok) {
        const data: Settings = await res.json()
        setSettings(data)
        setSavedSettings(data)
      }
    } catch {
      /* ignore */
    }
  }, [])

  useEffect(() => {
    fetchProviderTypes()
    fetchProviders()
    fetchSettings()
  }, [fetchProviderTypes, fetchProviders, fetchSettings])

  const handleProvidersChanged = useCallback(() => {
    fetchProviders()
    fetchSettings() // default_model may have been auto-set
  }, [fetchProviders, fetchSettings])

  return (
    <div className="flex h-full flex-1 flex-col">
      <div className="flex h-12 shrink-0 items-center border-b px-4">
        <span className="font-heading text-sm font-medium">Settings</span>
      </div>
      <div className="flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-2xl flex-col gap-6 p-6">
          <ClusterCard />

          <ProvidersCard
            providerItems={providerItems}
            providers={providers}
            onProvidersChanged={handleProvidersChanged}
          />

          <DefaultModelCard
            providerItems={providerItems}
            configuredProviders={Object.keys(providers)}
            settings={settings}
            savedSettings={savedSettings}
            onSettingsChange={setSettings}
            onSaved={setSavedSettings}
          />

          {/* Appearance */}
          <Card>
            <CardHeader>
              <CardTitle>Appearance</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="flex flex-col gap-2">
                <Label>Theme</Label>
                <Select
                  value={themeId}
                  onValueChange={(v) => {
                    if (v) setThemeId(v)
                  }}
                >
                  <SelectTrigger className="w-56">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {availableThemes
                      .filter((t) => t.type === "dark")
                      .map((t) => (
                        <SelectItem key={t.id} value={t.id}>{t.name}</SelectItem>
                      ))}
                    {availableThemes
                      .filter((t) => t.type === "light")
                      .map((t) => (
                        <SelectItem key={t.id} value={t.id}>{t.name}</SelectItem>
                      ))}
                  </SelectContent>
                </Select>
              </div>
            </CardContent>
          </Card>

          <GitIdentityCard />

          <SecurityCard />
        </div>
      </div>
    </div>
  )
}
