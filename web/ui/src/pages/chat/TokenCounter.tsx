import { cn } from "@/lib/utils"
import { formatTokenCount, formatCost } from "@/lib/format"
import type { UsageInfo } from "@/hooks/use-websocket"

export default function TokenCounter({ usage }: { usage: UsageInfo }) {
  const contextUsed = usage.prompt_tokens + usage.completion_tokens
  const pct = usage.context_window > 0
    ? (contextUsed / usage.context_window) * 100
    : 0
  const pctColor = pct > 80 ? "text-red-500" : pct > 60 ? "text-yellow-500" : "text-green-600"

  return (
    <div className="group relative">
      <div className="flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs tabular-nums text-muted-foreground cursor-default">
        <span>{formatTokenCount(contextUsed)}</span>
        <span>/</span>
        <span>{formatTokenCount(usage.context_window)}</span>
      </div>

      <div className="pointer-events-none absolute right-0 top-full z-50 mt-2 opacity-0 transition-opacity group-hover:pointer-events-auto group-hover:opacity-100">
        <div className="w-56 rounded-lg border bg-popover p-3 text-sm shadow-md">
          <table className="w-full">
            <tbody>
              <tr>
                <td className="py-0.5 text-muted-foreground">Context</td>
                <td className={cn("py-0.5 text-right tabular-nums font-medium", pctColor)}>
                  {pct.toFixed(1)}%
                </td>
              </tr>
              <tr>
                <td className="py-0.5 text-muted-foreground">Turn input</td>
                <td className="py-0.5 text-right tabular-nums">
                  {usage.turn_input_tokens.toLocaleString()}
                </td>
              </tr>
              <tr>
                <td className="py-0.5 text-muted-foreground">Turn output</td>
                <td className="py-0.5 text-right tabular-nums">
                  {usage.turn_output_tokens.toLocaleString()}
                </td>
              </tr>
              <tr>
                <td className="border-t pt-1.5 text-muted-foreground">Turn cost</td>
                <td className="border-t pt-1.5 text-right tabular-nums">
                  {formatCost(usage.turn_cost)}
                </td>
              </tr>
              {usage.session_cost > 0 && (
                <tr>
                  <td className="py-0.5 text-muted-foreground">Session cost</td>
                  <td className="py-0.5 text-right tabular-nums">
                    {formatCost(usage.session_cost)}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}
