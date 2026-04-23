import { useCallback, useEffect, useState } from "react"
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
import { Separator } from "@/components/ui/separator"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { HugeiconsIcon } from "@hugeicons/react"
import { Copy01Icon, Tick02Icon } from "@hugeicons/core-free-icons"

interface GitIdentity {
  name: string
  email: string
  has_key: boolean
  pubkey?: string
  hostname: string
}

export default function GitIdentityCard() {
  const [data, setData] = useState<GitIdentity | null>(null)
  const [name, setName] = useState("")
  const [email, setEmail] = useState("")
  const [saveMsg, setSaveMsg] = useState("")
  const [saveError, setSaveError] = useState(false)
  const [saving, setSaving] = useState(false)
  const [generating, setGenerating] = useState(false)
  const [copied, setCopied] = useState(false)
  const [regenerateOpen, setRegenerateOpen] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await fetch("/api/git-identity")
      if (!res.ok) return
      const d: GitIdentity = await res.json()
      setData(d)
      setName(d.name || "")
      setEmail(d.email || "")
    } catch {
      /* ignore */
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const dirty =
    data !== null && (name !== (data.name || "") || email !== (data.email || ""))

  const handleSave = async () => {
    setSaveMsg("")
    setSaveError(false)
    setSaving(true)
    try {
      const res = await fetch("/api/git-identity", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.trim(), email: email.trim() }),
      })
      if (res.ok) {
        setSaveMsg("Saved")
        await load()
      } else {
        setSaveMsg((await res.text()) || "Failed to save")
        setSaveError(true)
      }
    } catch {
      setSaveMsg("Unable to connect to the server")
      setSaveError(true)
    } finally {
      setSaving(false)
    }
  }

  const handleGenerate = async (force: boolean) => {
    setGenerating(true)
    try {
      const res = await fetch("/api/git-identity/ssh-key", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ force }),
      })
      if (res.ok) {
        await load()
      }
    } finally {
      setGenerating(false)
      setRegenerateOpen(false)
    }
  }

  const handleCopy = async () => {
    if (!data?.pubkey) return
    try {
      await navigator.clipboard.writeText(data.pubkey)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* ignore */
    }
  }

  const githubAddUrl = () => {
    const title = encodeURIComponent(`hiro@${data?.hostname || "hiro"}`)
    return `https://github.com/settings/ssh/new?title=${title}`
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Git &amp; SSH</CardTitle>
        <CardDescription>
          Identity and SSH key used by agents to clone, commit, and push.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-col gap-2">
          <Label>Name</Label>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Ada Lovelace"
          />
        </div>
        <div className="flex flex-col gap-2">
          <Label>Email</Label>
          <Input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="ada@example.com"
          />
        </div>
        {saveMsg && (
          <p
            className={`text-sm ${saveError ? "text-destructive" : "text-green-500"}`}
          >
            {saveMsg}
          </p>
        )}
        <Button
          onClick={handleSave}
          disabled={!dirty || saving || !name.trim() || !email.trim()}
          className="w-fit"
        >
          {saving ? "Saving…" : "Save"}
        </Button>

        <Separator />

        <div className="flex flex-col gap-2">
          <Label>SSH Key</Label>
          {data?.has_key && data.pubkey ? (
            <>
              <div className="relative">
                <pre className="bg-muted rounded-md border p-3 pr-12 text-xs break-all whitespace-pre-wrap">
                  {data.pubkey}
                </pre>
                <Button
                  variant="ghost"
                  size="icon"
                  className="absolute top-1 right-1"
                  onClick={handleCopy}
                  aria-label="Copy public key"
                >
                  <HugeiconsIcon icon={copied ? Tick02Icon : Copy01Icon} />
                </Button>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button
                  variant="outline"
                  onClick={() =>
                    window.open(githubAddUrl(), "_blank", "noreferrer")
                  }
                >
                  Add to GitHub
                </Button>
                <Button
                  variant="outline"
                  onClick={() => setRegenerateOpen(true)}
                >
                  Regenerate
                </Button>
              </div>
            </>
          ) : (
            <>
              <p className="text-muted-foreground text-sm">
                No SSH key yet. Generate one to let agents authenticate with
                GitHub and other Git hosts.
              </p>
              <Button
                onClick={() => handleGenerate(false)}
                disabled={generating}
                className="w-fit"
              >
                {generating ? "Generating…" : "Generate SSH key"}
              </Button>
            </>
          )}
          <p className="text-muted-foreground text-xs">
            For GitHub CLI, run <code>gh auth login</code> in a terminal —{" "}
            <code>gh</code> is already installed.
          </p>
        </div>
      </CardContent>

      <Dialog open={regenerateOpen} onOpenChange={setRegenerateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Regenerate SSH key?</DialogTitle>
            <DialogDescription>
              This overwrites your existing key. Any Git host trusting the old
              key will reject pushes until you upload the new one. Remember to
              remove the old key from GitHub afterward.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setRegenerateOpen(false)}
              disabled={generating}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => handleGenerate(true)}
              disabled={generating}
            >
              {generating ? "Regenerating…" : "Yes, regenerate"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
