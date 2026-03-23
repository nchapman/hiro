import { useState } from "react"
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
import { CheckCircle2, Loader2, XCircle } from "lucide-react"

interface SetupProps {
  onComplete: () => void
}

type Step = "password" | "provider" | "done"

const defaultModels: Record<string, string> = {
  anthropic: "claude-sonnet-4-20250514",
  openrouter: "anthropic/claude-sonnet-4-20250514",
}

export default function Setup({ onComplete }: SetupProps) {
  const [step, setStep] = useState<Step>("password")
  const [password, setPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [providerType, setProviderType] = useState("anthropic")
  const [apiKey, setApiKey] = useState("")
  const [model, setModel] = useState("")
  const [testStatus, setTestStatus] = useState<
    "idle" | "testing" | "success" | "error"
  >("idle")
  const [testError, setTestError] = useState("")
  const [error, setError] = useState("")
  const [submitting, setSubmitting] = useState(false)

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
          default_model: model || defaultModels[providerType] || "",
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
                      if (v) setProviderType(v)
                      setTestStatus("idle")
                    }}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="anthropic">Anthropic</SelectItem>
                      <SelectItem value="openrouter">OpenRouter</SelectItem>
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
                    placeholder={
                      providerType === "anthropic"
                        ? "sk-ant-..."
                        : "sk-or-..."
                    }
                  />
                </div>
                <div className="flex flex-col gap-2">
                  <Label htmlFor="setup-model">
                    Default Model{" "}
                    <span className="text-muted-foreground">(optional)</span>
                  </Label>
                  <Input
                    id="setup-model"
                    value={model}
                    onChange={(e) => setModel(e.target.value)}
                    placeholder={defaultModels[providerType] || ""}
                  />
                </div>

                <div className="flex items-center gap-2">
                  <Button
                    variant="outline"
                    onClick={handleTestProvider}
                    disabled={!apiKey || testStatus === "testing"}
                    className="flex-1"
                  >
                    {testStatus === "testing" && (
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    )}
                    Test Connection
                  </Button>
                  {testStatus === "success" && (
                    <Badge
                      variant="outline"
                      className="gap-1 border-green-500 text-green-500"
                    >
                      <CheckCircle2 className="h-3 w-3" />
                      Connected
                    </Badge>
                  )}
                  {testStatus === "error" && (
                    <Badge
                      variant="outline"
                      className="gap-1 border-destructive text-destructive"
                    >
                      <XCircle className="h-3 w-3" />
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
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
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
                <CheckCircle2 className="h-6 w-6 text-green-500" />
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
