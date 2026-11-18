import { Suspense, lazy } from "react"
import type { FeedbackListItem, FeedbackStatus, Overview } from "@/lib/api"
import { isOverdue, OVERDUE_HOURS } from "@/lib/sla"
import { ChartErrorBoundary } from "@/components/analytics/chart-error-boundary"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"

/**
 * Recharts is ~460 kB of the bundle and the only thing that needs it is the
 * trend column, so it loads on its own chunk after the numbers have painted.
 */
const TrendCard = lazy(() =>
  import("@/components/analytics/trend-card").then((m) => ({ default: m.TrendCard })),
)

function Delta({ value, suffix = "%" }: { value: number; suffix?: string }) {
  const tone =
    value === 0
      ? "bg-panel-2 text-muted-foreground"
      : value > 0
        ? "bg-status-resolved-bg text-status-resolved"
        : "bg-prio-urgent/12 text-prio-urgent"
  return (
    <span className={cn("tnum rounded-md px-1.5 font-mono text-[11px] font-medium", tone)}>
      {value === 0 ? "持平" : `${value > 0 ? "+" : ""}${value}${suffix}`}
    </span>
  )
}

function Metric({
  label,
  value,
  hint,
  tone,
  delta,
  onClick,
}: {
  label: string
  value: string
  hint: React.ReactNode
  tone?: string
  delta?: React.ReactNode
  onClick?: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!onClick}
      className="hover:bg-panel-2 focus-visible:ring-ring/50 grid gap-0.5 border-r px-4 py-3.5 text-left outline-none last:border-r-0 focus-visible:ring-[3px] disabled:hover:bg-transparent"
    >
      <span className="cap">{label}</span>
      <span className="flex items-baseline gap-2">
        <span className={cn("tnum text-[27px] leading-none font-bold tracking-[-0.04em]", tone)}>
          {value}
        </span>
        {delta}
      </span>
      <span className="text-muted-foreground text-xs">{hint}</span>
    </button>
  )
}

export function MetricsPanel({
  overview,
  items,
  onStatusChange,
}: {
  overview: Overview | null
  /** Rows currently loaded in the list — the source for the derived figures. */
  items: FeedbackListItem[]
  onStatusChange: (status?: FeedbackStatus) => void
}) {
  if (!overview) {
    return <Skeleton className="h-[92px] rounded-2xl" />
  }

  const last7 = overview.trend.slice(-7).reduce((sum, d) => sum + d.count, 0)
  const prev7 = overview.trend.slice(-14, -7).reduce((sum, d) => sum + d.count, 0)
  const deltaPct = prev7 > 0 ? Math.round(((last7 - prev7) / prev7) * 100) : 0

  const overdue = items.filter(isOverdue).length

  const rated = items.filter((i) => typeof i.rating === "number")
  const average = rated.length
    ? (rated.reduce((sum, i) => sum + (i.rating ?? 0), 0) / rated.length).toFixed(1)
    : "—"

  return (
    <section className="bg-card grid overflow-hidden rounded-2xl border shadow-lift sm:grid-cols-2 lg:grid-cols-[repeat(4,1fr)_1.5fr]">
      <Metric
        label="总反馈"
        value={overview.total.toLocaleString("zh-CN")}
        delta={prev7 > 0 ? <Delta value={deltaPct} /> : undefined}
        hint={`近 7 天 ${last7} · 前 7 天 ${prev7}`}
        onClick={() => onStatusChange(undefined)}
      />
      <Metric
        label="待处理"
        value={String(overview.by_status.open ?? 0)}
        tone="text-status-open"
        delta={
          overdue > 0 ? (
            <span className="tnum bg-prio-urgent/12 text-prio-urgent rounded-md px-1.5 font-mono text-[11px] font-medium">
              {overdue} 超时
            </span>
          ) : undefined
        }
        hint={
          overdue > 0 ? (
            <>
              已加载的行里 <span className="text-prio-urgent font-semibold">{overdue} 条</span>
              超过 {OVERDUE_HOURS} 小时
            </>
          ) : (
            "等待首次响应"
          )
        }
        onClick={() => onStatusChange("open")}
      />
      <Metric
        label="处理中"
        value={String(overview.by_status.in_progress ?? 0)}
        tone="text-status-progress"
        hint="已认领"
        onClick={() => onStatusChange("in_progress")}
      />
      <Metric
        label="满意度"
        value={average}
        tone="text-status-resolved"
        hint={rated.length ? `已加载 ${rated.length} 条评分 · 满分 5` : "已加载的行里还没有评分"}
      />
      <div className="grid gap-1 px-4 py-3.5">
        <ChartErrorBoundary>
          <Suspense fallback={<Skeleton className="h-[68px] rounded-md" />}>
            <TrendCard trend={overview.trend} />
          </Suspense>
        </ChartErrorBoundary>
      </div>
    </section>
  )
}
