import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

// randomId returns a unique-enough ID for client-side React keys and local
// state. Uses crypto.randomUUID when available; otherwise falls back to a
// Math.random-based ID. crypto.randomUUID is only exposed on secure contexts
// (HTTPS or localhost), so plain-HTTP deployments over LAN/VPN need the
// fallback.
export function randomId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID()
  }
  return `id-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}
