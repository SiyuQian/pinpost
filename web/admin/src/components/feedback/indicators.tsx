import { ChevronDown, Circle, Square, Star, TriangleAlert } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import type { FeedbackPriority, FeedbackSource, FeedbackStatus } from "@/lib/api"
import {
  PRIORITY_CLASS,
  PRIORITY_LABEL,
  SOURCE_CLASS,
  SOURCE_LABEL,
  STATUS_CLASS,
  STATUS_LABEL,
} from "@/lib/labels"

/**
 * Status as a filled dot + label, so state reads without relying on colour
 * alone. Built on Badge so the pill geometry (radius, padding, focus ring)
 * comes from the design system; only the status tone is layered on top.
 */
export function StatusBadge({ status, className }: { status: FeedbackStatus; className?: string }) {
  return (
    <Badge
      variant="secondary"
      className={cn(
        "h-[23px] gap-1.5 rounded-[7px] px-2.5 font-mono text-[10.5px] font-medium tracking-wide",
        STATUS_CLASS[status],
        className,
      )}
    >
      <span className="size-[5px] rounded-full bg-current" aria-hidden />
      {STATUS_LABEL[status]}
    </Badge>
  )
}

const PRIORITY_ICON: Record<Exclude<FeedbackPriority, "none">, React.ReactNode> = {
  urgent: <Circle className="size-3" fill="currentColor" />,
  high: <TriangleAlert className="size-3" />,
  medium: <Square className="size-3" />,
  low: <ChevronDown className="size-3" />,
}

export function PriorityTag({ priority }: { priority: FeedbackPriority }) {
  if (priority === "none") return <span className="text-muted-foreground text-xs">—</span>
  return (
    <Badge variant="ghost" className={cn("px-0", PRIORITY_CLASS[priority])}>
      <span aria-hidden>{PRIORITY_ICON[priority]}</span>
      {PRIORITY_LABEL[priority]}
    </Badge>
  )
}

/**
 * The glyph run is decorative — the score lives in the label, so a screen
 * reader reads "3 星" rather than five identical stars. The trigger is
 * focusable so the tooltip is reachable without a pointer.
 */
export function Rating({ value }: { value?: number | null }) {
  if (!value) return <span className="text-muted-foreground text-xs">—</span>
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          className="text-status-progress text-xs tracking-tight"
          tabIndex={0}
          role="img"
          aria-label={`${value} 星`}
        >
          <span aria-hidden className="inline-flex items-center gap-px">
            {Array.from({ length: 5 }, (_, i) =>
              i < value ? (
                <Star key={i} className="size-3" fill="currentColor" />
              ) : (
                <Star key={i} className="text-muted-foreground/40 size-3" />
              ),
            )}
          </span>
        </span>
      </TooltipTrigger>
      <TooltipContent>{value} 星</TooltipContent>
    </Tooltip>
  )
}

/**
 * Marks a row the team filed itself. Only `internal` renders: widget reports
 * are the overwhelming majority and the default reading, so badging them too
 * would add noise to every row without telling anyone anything.
 */
export function SourceChip({ source }: { source: FeedbackSource }) {
  if (source !== "internal") return null
  return (
    <Badge variant="secondary" className={cn("rounded-sm px-1.5 text-[11px] font-medium", SOURCE_CLASS[source])}>
      {SOURCE_LABEL[source]}
    </Badge>
  )
}

export function TagChip({ children }: { children: React.ReactNode }) {
  return (
    <Badge variant="secondary" className="rounded-sm px-1.5 text-[11px] font-normal">
      {children}
    </Badge>
  )
}
