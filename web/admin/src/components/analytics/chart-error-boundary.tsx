import { Component, type ErrorInfo, type ReactNode } from "react"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"

/**
 * The trend card is a lazily-loaded chunk, so its import can reject — offline,
 * or the classic Vite case of a stale tab asking for a hashed chunk that a
 * redeploy has removed. Without a boundary that rejection unmounts the whole
 * React root, taking the triage console down for a decorative sparkline.
 */
export class ChartErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false }

  static getDerivedStateFromError() {
    return { failed: true }
  }

  /**
   * Neither failure mode above reproduces locally, so the thrown error is the
   * only evidence that either happened — swallowing it leaves nothing to read
   * from an operator's console, and nothing for telemetry to hook later.
   */
  componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error("趋势图渲染失败", error, info.componentStack)
  }

  render() {
    if (this.state.failed) {
      return (
        <Card className="text-muted-foreground grid place-items-center gap-1 p-4 text-xs">
          趋势图加载失败
          {/* A reload rather than clearing `failed`: React.lazy caches the
              rejected promise, so remounting the same component re-throws at
              once. Reloading also re-fetches index.html, which is the actual
              fix for the stale-chunk case — the new hashes come with it. */}
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={() => window.location.reload()}
          >
            重新加载
          </Button>
        </Card>
      )
    }
    return this.props.children
  }
}
