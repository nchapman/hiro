import { useState, useEffect, useMemo } from "react"
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
import { Badge } from "@/components/ui/badge"
import { HugeiconsIcon } from "@hugeicons/react"
import { CheckmarkCircle01Icon, Loading02Icon, CancelCircleIcon } from "@hugeicons/core-free-icons"

import type { ModelInfo } from "@/lib/chat-types"

interface SetupProps {
  onComplete: () => void
}

type Step = "password" | "provider" | "done"

interface ProviderTypeInfo {
  id: string
  name: string
}

export default function Setup({ onComplete }: SetupProps) {
  const [step, setStep] = useState<Step>("password")
  const [password, setPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [providerTypes, setProviderTypes] = useState<ProviderTypeInfo[]>([])
  const [providerType, setProviderType] = useState("")
  const [providerModels, setProviderModels] = useState<ModelInfo[]>([])
  const [apiKey, setApiKey] = useState("")
  const [model, setModel] = useState("")
  const [testStatus, setTestStatus] = useState<
    "idle" | "testing" | "success" | "error"
  >("idle")
  const [testError, setTestError] = useState("")
  const [error, setError] = useState("")
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    fetch("/api/setup/provider-types")
      .then((res) => (res.ok ? res.json() : []))
      .then((types: ProviderTypeInfo[]) => {
        setProviderTypes(types)
        if (types.length > 0 && !providerType) {
          // Default to anthropic if available, otherwise first
          const anthro = types.find((t) => t.id === "anthropic")
          setProviderType(anthro ? anthro.id : types[0].id)
        }
      })
      .catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Fetch models when the selected provider changes.
  useEffect(() => {
    if (!providerType) {
      setProviderModels([])
      return
    }
    fetch(`/api/setup/models?provider=${encodeURIComponent(providerType)}`)
      .then((res) => (res.ok ? res.json() : []))
      .then((data: ModelInfo[]) => setProviderModels(data ?? []))
      .catch(() => setProviderModels([]))
  }, [providerType])

  const providerItems = useMemo(
    () => Object.fromEntries(providerTypes.map((p) => [p.id, p.name])),
    [providerTypes]
  )

  const passwordValid = password.length >= 8 && password === confirmPassword

  const handleTestProvider = async () => {
    setTestStatus("testing")
    setTestError("")

    try {
      const res = await fetch("/api/setup/test-provider", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          type: providerType,
          api_key: apiKey,
          model: model || undefined,
        }),
      })
      const data = await res.json()
      if (data.valid) {
        setTestStatus("success")
      } else {
        setTestStatus("error")
        setTestError(data.error || "Connection failed")
      }
    } catch {
      setTestStatus("error")
      setTestError("Network error")
    }
  }

  const handleFinish = async () => {
    setError("")
    setSubmitting(true)

    try {
      const res = await fetch("/api/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          password,
          provider_type: providerType,
          api_key: apiKey,
          default_model: model || "",
        }),
      })

      if (res.ok) {
        setStep("done")
        setTimeout(onComplete, 1500)
      } else {
        const text = await res.text()
        setError(text || "Setup failed")
      }
    } catch {
      setError("Connection failed")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        {step === "password" && (
          <>
            <CardHeader>
              <CardTitle className="text-2xl font-bold">
                Welcome to Hive
              </CardTitle>
              <CardDescription>
                Set an admin password to secure your instance.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <form
                onSubmit={(e) => {
                  e.preventDefault()
                  if (passwordValid) setStep("provider")
                }}
                className="flex flex-col gap-4"
              >
                <div className="flex flex-col gap-2">
                  <Label htmlFor="setup-password">Password</Label>
                  <Input
                    id="setup-password"
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    placeholder="At least 8 characters"
                    autoFocus
                  />
                </div>
                <div className="flex flex-col gap-2">
                  <Label htmlFor="setup-confirm">Confirm password</Label>
                  <Input
                    id="setup-confirm"
                    type="password"
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    placeholder="Confirm password"
                  />
                  {confirmPassword && password !== confirmPassword && (
                    <p className="text-sm text-destructive">
                      Passwords don't match
                    </p>
                  )}
                </div>
                <Button
                  type="submit"
                  disabled={!passwordValid}
                  className="w-full"
                >
                  Continue
                </Button>
              </form>
            </CardContent>
          </>
        )}

        {step === "provider" && (
          <>
            <CardHeader>
              <CardTitle>Configure LLM Provider</CardTitle>
              <CardDescription>
                Connect your AI provider to power your agents.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <form
                onSubmit={(e) => {
                  e.preventDefault()
                  if (apiKey && !submitting) handleFinish()
                }}
                className="flex flex-col gap-4"
              >
                <div className="flex flex-col gap-2">
                  <Label>Provider</Label>
                  <Select
                    value={providerType}
                    onValueChange={(v) => {
                      if (v) {
                        setProviderType(v)
                        setModel("")
                      }
                      setTestStatus("idle")
                    }}
                    items={providerItems}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {providerTypes.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="flex flex-col gap-2">
                  <Label htmlFor="setup-apikey">API Key</Label>
                  <Input
                    id="setup-apikey"
                    type="password"
                    value={apiKey}
                    onChange={(e) => {
                      setApiKey(e.target.value)
                      setTestStatus("idle")
                    }}
                    placeholder="Enter your API key"
                  />
                </div>
                {providerModels.length > 0 && (
                  <div className="flex flex-col gap-2">
                    <Label>
                      Default Model{" "}
                      <span className="text-muted-foreground">(optional)</span>
                    </Label>
                    <Select
                      value={model}
                      onValueChange={(v) => setModel(v ?? "")}
                      items={Object.fromEntries(
                        providerModels.map((m) => [m.id, m.name || m.id])
                      )}
                    >
                      <SelectTrigger className="w-full">
                        <SelectValue placeholder="Provider default" />
                      </SelectTrigger>
                      <SelectContent>
                        {providerModels.map((m) => (
                          <SelectItem key={m.id} value={m.id}>
                            {m.name || m.id}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                )}

                <div className="flex items-center gap-2">
                  <Button
                    variant="outline"
                    onClick={handleTestProvider}
                    disabled={!apiKey || testStatus === "testing"}
                    className="flex-1"
                  >
                    {testStatus === "testing" && (
                      <HugeiconsIcon icon={Loading02Icon} className="mr-2 h-4 w-4 animate-spin" />
                    )}
                    Test Connection
                  </Button>
                  {testStatus === "success" && (
                    <Badge
                      variant="outline"
                      className="gap-1 border-green-500 text-green-500"
                    >
                      <HugeiconsIcon icon={CheckmarkCircle01Icon} className="h-3 w-3" />
                      Connected
                    </Badge>
                  )}
                  {testStatus === "error" && (
                    <Badge
                      variant="outline"
                      className="gap-1 border-destructive text-destructive"
                    >
                      <HugeiconsIcon icon={CancelCircleIcon} className="h-3 w-3" />
                      Failed
                    </Badge>
                  )}
                </div>
                {testError && (
                  <p className="text-sm text-destructive">{testError}</p>
                )}

                {error && <p className="text-sm text-destructive">{error}</p>}

                <div className="flex gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => setStep("password")}
                    className="flex-1"
                  >
                    Back
                  </Button>
                  <Button
                    type="submit"
                    disabled={!apiKey || submitting}
                    className="flex-1"
                  >
                    {submitting ? (
                      <HugeiconsIcon icon={Loading02Icon} className="mr-2 h-4 w-4 animate-spin" />
                    ) : null}
                    Complete Setup
                  </Button>
                </div>
              </form>
            </CardContent>
          </>
        )}

        {step === "done" && (
          <>
            <CardHeader className="text-center">
              <div className="mx-auto mb-2 flex h-12 w-12 items-center justify-center rounded-full bg-green-500/10">
                <HugeiconsIcon icon={CheckmarkCircle01Icon} className="h-6 w-6 text-green-500" />
              </div>
              <CardTitle>You're all set!</CardTitle>
              <CardDescription>
                Hive is configured and ready to use.
              </CardDescription>
            </CardHeader>
          </>
        )}
      </Card>
    </div>
  )
}
