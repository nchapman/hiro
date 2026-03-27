import type { SessionInfo } from "@/App"

export function statusDotColor(session: SessionInfo): string {
  if (session.status === "stopped") return "bg-gray-400"
  if (session.mode === "ephemeral") return "bg-violet-500"
  return "bg-green-500"
}
