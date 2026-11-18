import { useState } from "react"
import { Check, X } from "lucide-react"
import type { SetupState } from "@/lib/api"
import { comingSoon } from "@/components/shell/coming-soon"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

const KEY = "pinpost-setup-dismissed"

/**
 * Onboarding progress. Every step comes from `/v1/setup`, which derives it from
 * what the workspace actually contains — feedback received, a second member, an
 * active webhook. Steps the API cannot prove are not listed at all rather than
 * shown as undone.
 */
export function SetupChecklist({
  setup,
  onOpenEmbed,
  onOpenAutomation,
}: {
  setup: SetupState | null
  onOpenEmbed: () => void
  onOpenAutomation: () => void
}) {
  const [dismissed, setDismissed] = useState(() => localStorage.getItem(KEY) === "1")

  if (!setup || dismissed) return null

  // Each step opens the page where it gets done; 邀请成员 has no page yet.
  const steps = [
    {
      label: "装上反馈组件",
      done: setup.install_widget,
      detail: `已收到 ${setup.feedback_total} 条反馈`,
      open: onOpenEmbed,
    },
    { label: "邀请成员", done: setup.invite_members, detail: `${setup.member_count} 位成员`, open: comingSoon },
    {
      label: "接通知 Webhook",
      done: setup.connect_notifications,
      detail: setup.endpoint_count > 0 ? `${setup.endpoint_count} 个在用` : "新反馈还没人收到通知",
      open: onOpenAutomation,
    },
  ]
  const done = steps.filter((s) => s.done).length
  const next = steps.find((s) => !s.done)

  if (!next) return null

  const circumference = 2 * Math.PI * 15

  return (
    <section className="bg-card shadow-lift flex flex-wrap items-center gap-4 rounded-2xl border p-3.5">
      <span className="relative grid size-[38px] shrink-0 place-items-center">
        <svg viewBox="0 0 38 38" className="size-[38px]" aria-hidden>
          <circle cx="19" cy="19" r="15" fill="none" stroke="var(--border)" strokeWidth="4" />
          <circle
            cx="19"
            cy="19"
            r="15"
            fill="none"
            stroke="var(--primary)"
            strokeWidth="4"
            strokeLinecap="round"
            strokeDasharray={circumference}
            strokeDashoffset={circumference * (1 - done / steps.length)}
            transform="rotate(-90 19 19)"
          />
        </svg>
        <span className="tnum absolute font-mono text-[10px] font-bold">
          {done}/{steps.length}
        </span>
      </span>

      <span className="min-w-0">
        <span className="block text-[13.5px] font-semibold">把 Pinpost 接完整</span>
        <span className="text-muted-foreground block text-[12.5px]">
          下一步：{next.label} —— {next.detail}
        </span>
      </span>

      <span className="ml-auto flex flex-wrap gap-1.5">
        {steps.map((step) => (
          <button
            key={step.label}
            type="button"
            onClick={step.open}
            title={step.detail}
            className={cn(
              "focus-visible:ring-ring/50 inline-flex h-7 items-center gap-1.5 rounded-full border px-3 text-[12.5px] outline-none focus-visible:ring-[3px]",
              step.done
                ? "bg-status-resolved-bg text-status-resolved border-transparent"
                : step === next
                  ? "border-primary bg-primary/10 text-primary font-semibold"
                  : "bg-panel-2 text-muted-foreground",
            )}
          >
            {step.done && <Check className="size-3" />}
            {step.label}
          </button>
        ))}
      </span>

      <Button
        variant="ghost"
        size="icon-sm"
        aria-label="收起上手清单"
        onClick={() => {
          localStorage.setItem(KEY, "1")
          setDismissed(true)
        }}
      >
        <X />
      </Button>
    </section>
  )
}
