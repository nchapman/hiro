import { useState, useEffect, useCallback, useMemo } from "react"
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
import { Separator } from "@/components/ui/separator"
import { ScrollArea } from "@/components/ui/scroll-area"
import { useTheme } from "@/hooks/use-theme"
import {
  Plus,
  Trash2,
  CheckCircle2,
  XCircle,
  Loader2,
} from "lucide-react"

import type { ModelInfo } from "@/lib/chat-types"

interface ProviderInfo {
  api_key: string
  base_url?: string
}

interface ProviderTypeInfo {
  id: string
  name: string
}

interface Settings {
  default_provider: string
  default_model: string
}

export default function SettingsPage() {
  const { theme, setTheme } = useTheme()
  const [providerTypes, setProviderTypes] = useState<ProviderTypeInfo[]>([])
  const [providers, setProviders] = useState<Record<string, ProviderInfo>>({})
  const [settings, setSettings] = useState<Settings>({
    default_provider: "",
    default_model: "",
  })
  const [savedSettings, setSavedSettings] = useState<Settings>({
    default_provider: "",
    default_model: "",
  })

  // Password change
  const [currentPassword, setCurrentPassword] = useState("")
  const [newPassword, setNewPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [passwordMsg, setPasswordMsg] = useState("")
  const [passwordError, setPasswordError] = useState(false)

  // Add provider dialog
  const [addType, setAddType] = useState("")
  const [addKey, setAddKey] = useState("")
  const [addOpen, setAddOpen] = useState(false)

  // Models for the selected default provider
  const [defaultModels, setDefaultModels] = useState<ModelInfo[]>([])

  // Test status per provider
  const [testStatus, setTestStatus] = useState<
    Record<string, "idle" | "testing" | "success" | "error">
  >({})
  const [testErrors, setTestErrors] = useState<Record<string, string>>({})

  const fetchProviderTypes = useCallback(async () => {
    try {
      const res = await fetch("/api/provider-types")
      if (res.ok) setProviderTypes(await res.json())
    } catch {
      /* ignore */
    }
  }, [])

  // Map of provider ID → display name for Select items prop.
  const providerItems = useMemo(
    () => Object.fromEntries(providerTypes.map((p) => [p.id, p.name])),
    [providerTypes]
  )

  const providerLabel = useCallback(
    (type: string) => providerItems[type] ?? type,
    [providerItems]
  )

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

  const handleSaveSettings = async () => {
    try {
      const res = await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(settings),
      })
      if (!res.ok) return
      setSavedSettings(settings)
    } catch (e) { console.error("settings operation failed:", e) }
  }

  // Filter add dialog to provider types not already configured
  const availableTypes = providerTypes.filter(
    (p) => !providers[p.id]
  )

  const handleAddProvider = async () => {
    if (!addType || !addKey) return
    try {
      const res = await fetch(`/api/settings/providers/${encodeURIComponent(addType)}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ api_key: addKey }),
      })
      if (!res.ok) return
      setAddType("")
      setAddKey("")
      setAddOpen(false)
      fetchProviders()
      fetchSettings() // default_provider may have been auto-set
    } catch (e) { console.error("settings operation failed:", e) }
  }

  const handleDeleteProvider = async (type: string) => {
    try {
      const res = await fetch(`/api/settings/providers/${encodeURIComponent(type)}`, {
        method: "DELETE",
      })
      if (!res.ok) return
      fetchProviders()
      fetchSettings()
    } catch (e) { console.error("settings operation failed:", e) }
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

  const handleChangePassword = async () => {
    setPasswordMsg("")
    setPasswordError(false)
    if (newPassword.length < 8) {
      setPasswordMsg("Password must be at least 8 characters")
      setPasswordError(true)
      return
    }
    if (newPassword !== confirmPassword) {
      setPasswordMsg("Passwords don't match")
      setPasswordError(true)
      return
    }

    const res = await fetch("/api/auth/password", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ current: currentPassword, new: newPassword }),
    })

    if (res.ok) {
      setPasswordMsg("Password changed successfully")
      setCurrentPassword("")
      setNewPassword("")
      setConfirmPassword("")
    } else {
      setPasswordMsg("Current password is incorrect")
      setPasswordError(true)
    }
  }

  const configuredTypes = Object.keys(providers)

  return (
    <ScrollArea className="flex-1">
      <div className="mx-auto max-w-2xl space-y-6 p-6">
        <h1 className="text-2xl font-bold">Settings</h1>

        {/* Providers */}
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
                      <Plus className="h-4 w-4" />
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
                            {availableTypes.map((p) => (
                              <SelectItem key={p.id} value={p.id}>
                                {p.name}
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
                          <CheckCircle2 className="h-4 w-4 text-green-500" />
                        )}
                        {testStatus[type_] === "error" && (
                          <XCircle className="h-4 w-4 text-destructive" />
                        )}
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => handleTestProvider(type_)}
                          disabled={testStatus[type_] === "testing"}
                        >
                          {testStatus[type_] === "testing" ? (
                            <Loader2 className="h-4 w-4 animate-spin" />
                          ) : (
                            "Test"
                          )}
                        </Button>
                        {configuredTypes.length > 1 && (
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8 text-destructive"
                            onClick={() => handleDeleteProvider(type_)}
                          >
                            <Trash2 className="h-4 w-4" />
                          </Button>
                        )}
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

        {/* Default Model */}
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
                      setSettings((s) => ({
                        ...s,
                        default_provider: v,
                        default_model: "",
                      }))
                  }}
                  items={providerItems}
                >
                  <SelectTrigger className="w-40">
                    <SelectValue placeholder="Select..." />
                  </SelectTrigger>
                  <SelectContent>
                    {configuredTypes.map((type_) => (
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
                      setSettings((s) => ({ ...s, default_model: v }))
                  }}
                  items={Object.fromEntries(
                    defaultModels.map((m) => [m.id, m.name || m.id])
                  )}
                >
                  <SelectTrigger>
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
              onClick={handleSaveSettings}
              disabled={!settingsChanged}
              variant="outline"
              className="w-fit"
            >
              Save
            </Button>
          </CardContent>
        </Card>

        {/* Appearance */}
        <Card>
          <CardHeader>
            <CardTitle>Appearance</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex flex-col gap-2">
              <Label>Theme</Label>
              <Select
                value={theme}
                onValueChange={(v) => {
                  if (v) setTheme(v as "light" | "dark" | "system")
                }}
              >
                <SelectTrigger className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="system">System</SelectItem>
                  <SelectItem value="light">Light</SelectItem>
                  <SelectItem value="dark">Dark</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </CardContent>
        </Card>

        {/* Security */}
        <Card>
          <CardHeader>
            <CardTitle>Security</CardTitle>
            <CardDescription>Change your admin password.</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label>Current Password</Label>
              <Input
                type="password"
                value={currentPassword}
                onChange={(e) => setCurrentPassword(e.target.value)}
              />
            </div>
            <Separator />
            <div className="flex flex-col gap-2">
              <Label>New Password</Label>
              <Input
                type="password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                placeholder="At least 8 characters"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label>Confirm New Password</Label>
              <Input
                type="password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
              />
            </div>
            {passwordMsg && (
              <p
                className={`text-sm ${passwordError ? "text-destructive" : "text-green-500"}`}
              >
                {passwordMsg}
              </p>
            )}
            <Button
              onClick={handleChangePassword}
              disabled={!currentPassword || !newPassword || !confirmPassword}
              className="w-fit"
            >
              Change Password
            </Button>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>
  )
}
