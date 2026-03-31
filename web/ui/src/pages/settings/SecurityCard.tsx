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
import { Separator } from "@/components/ui/separator"

export default function SecurityCard() {
  const [currentPassword, setCurrentPassword] = useState("")
  const [newPassword, setNewPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [passwordMsg, setPasswordMsg] = useState("")
  const [passwordError, setPasswordError] = useState(false)

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

    try {
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
    } catch {
      setPasswordMsg("Unable to connect to the server")
      setPasswordError(true)
    }
  }

  return (
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
  )
}
