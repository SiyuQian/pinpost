import { Area, AreaChart, CartesianGrid, XAxis } from "recharts"
import type { Overview } from "@/lib/api"
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart"

const TREND_CONFIG = {
  count: { label: "提交量", color: "var(--primary)" },
} satisfies ChartConfig

/**
 * The API returns date-only days ("2026-08-16"). `new Date()` reads those as UTC
 * midnight, so formatting in local time slides the label a day backwards for
 * anyone west of UTC — format in UTC to get the day the server meant.
 */
function dayLabel(day: string) {
  return new Date(day).toLocaleDateString("zh-CN", {
    month: "numeric",
    day: "numeric",
    timeZone: "UTC",
  })
}

/**
 * 14-day submission trend. An area chart rather than bare bars, so the shape of
 * the fortnight reads at a glance; hovering a day gives the exact count.
 */
export function TrendCard({ trend }: { trend: Overview["trend"] }) {
  const last = trend.at(-1)
  const data = trend.map((d) => ({ ...d, label: dayLabel(d.day) }))
  const peak = Math.max(0, ...trend.map((d) => d.count))
  const total = trend.reduce((sum, d) => sum + d.count, 0)

  return (
    <>
      <div className="flex items-baseline justify-between gap-2">
        <span className="cap">近 14 天</span>
        {last && <span className="tnum text-muted-foreground text-xs">今日 {last.count} 条</span>}
      </div>
      <div>
        {/* The summary the old hand-drawn sparkline carried goes on the chart
            itself, not on ChartContainer: `accessibilityLayer` (default-on in
            recharts 3 for cartesian charts, kept explicit because this depends
            on it) makes the root <svg> a focusable role="application" with
            arrow-key stepping through the days. A role="img" on the wrapping
            div would prune that whole subtree from the accessibility tree and
            leave a silent tab stop. aria-label is on recharts' SVG prop
            allowlist, so it lands on that same <svg> as its accessible name. */}
        <ChartContainer config={TREND_CONFIG} className="aspect-auto h-[52px] w-full">
          <AreaChart
            accessibilityLayer
            aria-label={`近 14 天提交量趋势，共 ${total} 条，单日峰值 ${peak} 条`}
            data={data}
            margin={{ top: 4, right: 0, bottom: 0, left: 0 }}
          >
            <CartesianGrid vertical={false} />
            <XAxis dataKey="label" hide />
            {/* No labelKey: it would resolve against TREND_CONFIG and come back
                empty, leaving the tooltip with a count and no date. */}
            <ChartTooltip cursor={false} content={<ChartTooltipContent indicator="line" />} />
            <Area
              dataKey="count"
              type="monotone"
              stroke="var(--color-count)"
              strokeWidth={1.8}
              fill="var(--color-count)"
              fillOpacity={0.2}
              dot={false}
              activeDot={{ r: 3 }}
              isAnimationActive={false}
            />
          </AreaChart>
        </ChartContainer>
      </div>
    </>
  )
}
