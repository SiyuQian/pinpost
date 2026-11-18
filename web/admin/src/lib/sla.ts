import type { FeedbackListItem } from "@/lib/api"
import { hoursSince } from "@/lib/format"

/**
 * Hours an untouched report may wait before it counts as overdue. The 待处理
 * tile's badge and the 已超时 saved view are the same claim about the same
 * rows, so they read the same constant and the same predicate — two copies
 * drift the moment the SLA changes and leave the panel contradicting itself.
 */
export const OVERDUE_HOURS = 24

export function isOverdue(item: FeedbackListItem) {
  return item.status === "open" && hoursSince(item.created_at) > OVERDUE_HOURS
}
