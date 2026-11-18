import { ArrowRight } from "lucide-react"
import type { FeedbackListItem, Member } from "@/lib/api"
import { formatRelative, hoursSince } from "@/lib/format"
import { comingSoon } from "@/components/shell/coming-soon"

/**
 * Who the open work is sitting with. Counted over the rows currently loaded —
 * the API has no per-assignee aggregate — so the heading says "已加载" and the
 * bars are relative to the largest queue rather than to any quota.
 */
export function TeamLoad({ items, members }: { items: FeedbackListItem[]; members: Member[] }) {
  const pending = items.filter((i) => i.status === "open" || i.status === "in_progress")

  const queues = [
    ...members.map((m) => ({
      id: m.ID,
      name: m.Name || m.Email,
      rows: pending.filter((i) => i.assignee_id === m.ID),
    })),
    { id: "unassigned", name: "未分配", rows: pending.filter((i) => !i.assignee_id) },
  ]
    .filter((q) => q.rows.length > 0)
    .sort((a, b) => b.rows.length - a.rows.length)

  const peak = Math.max(1, ...queues.map((q) => q.rows.length))

  return (
    <section className="bg-card grid gap-2.5 rounded-2xl border p-4 shadow-lift">
      <div className="flex items-center gap-2">
        <span className="text-[13px] font-semibold">团队负载</span>
        <button type="button" onClick={comingSoon} className="text-primary ml-auto flex items-center gap-1 text-xs font-semibold">
          成员与权限
          <ArrowRight className="size-3.5" />
        </button>
      </div>

      {queues.length === 0 ? (
        <span className="text-muted-foreground text-[12.5px]">已加载的行里没有待处理的反馈。</span>
      ) : (
        queues.map((queue) => {
          const oldest = queue.rows.reduce(
            (worst, row) => (hoursSince(row.created_at) > hoursSince(worst.created_at) ? row : worst),
            queue.rows[0]!,
          )
          const unassigned = queue.id === "unassigned"
          return (
            <div key={queue.id} className="grid gap-1.5">
              <div className="text-muted-foreground flex text-[12.5px]">
                <span>
                  <span className="text-foreground font-semibold">{queue.name}</span>
                  {unassigned ? " · 需要认领" : " · 待处理"}
                </span>
                <span className="tnum ml-auto font-mono text-[11.5px]">
                  {queue.rows.length} 条 · 最久 {formatRelative(oldest.created_at)}
                </span>
              </div>
              <div className="bg-panel-3 h-[5px] overflow-hidden rounded-full">
                <div
                  className={
                    unassigned
                      ? "from-prio-urgent to-status-progress h-full rounded-full bg-linear-to-r"
                      : "from-primary to-brand-deep h-full rounded-full bg-linear-to-r"
                  }
                  style={{ width: `${(queue.rows.length / peak) * 100}%` }}
                />
              </div>
            </div>
          )
        })
      )}
    </section>
  )
}
