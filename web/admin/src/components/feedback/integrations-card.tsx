import { ArrowRight } from "lucide-react"
import { comingSoon } from "@/components/shell/coming-soon"
import { cn } from "@/lib/utils"

/**
 * Integrations drawn on the feedback page. Webhook is the one that is wired —
 * it has a full backend and a management page in the 自动化 tab — so its chip
 * routes there. Slack, GitHub, Linear and 企业微信 have no backend yet, so
 * each is an add affordance (`＋ 名称`) that toasts rather than pretending.
 *
 * A chip only renders a connection state when it is given a real one: the
 * Webhook highlight comes from `webhookConnected`, read from the endpoint
 * count. Never hardcode one.
 */
const CHIPS: { name: string; live: boolean }[] = [
  { name: "Webhook", live: true },
  { name: "Slack", live: false },
  { name: "GitHub", live: false },
  { name: "Linear", live: false },
  { name: "企业微信", live: false },
]

export function IntegrationsCard({
  onOpenAutomation,
  onOpenEmbed,
  webhookConnected,
}: {
  onOpenAutomation?: () => void
  /** 嵌入与集成 lists every integration and its state. */
  onOpenEmbed?: () => void
  webhookConnected?: boolean
}) {
  return (
    <section className="bg-card grid gap-2.5 rounded-2xl border p-4 shadow-lift">
      <div className="flex items-center gap-2">
        <span className="text-[13px] font-semibold">集成</span>
        <button type="button" onClick={onOpenEmbed ?? comingSoon} className="text-primary ml-auto flex items-center gap-1 text-xs font-semibold">
          全部集成
          <ArrowRight className="size-3.5" />
        </button>
      </div>
      <div className="flex flex-wrap gap-2">
        {CHIPS.map(({ name, live }) => (
          <button
            key={name}
            type="button"
            onClick={live ? (onOpenAutomation ?? comingSoon) : comingSoon}
            className={cn(
              "focus-visible:ring-ring/50 inline-flex h-[30px] items-center gap-1.5 rounded-full border border-dashed px-3 text-[12.5px] outline-none focus-visible:ring-[3px]",
              live && webhookConnected
                ? "border-primary bg-primary/10 text-primary font-medium"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {live ? name : `＋ ${name}`}
          </button>
        ))}
      </div>
      <span className="text-muted-foreground text-[12.5px]">
        接上以后：新反馈进 Slack、一键转 GitHub Issue，Webhook 把反馈推到你自己的系统。
      </span>
    </section>
  )
}
