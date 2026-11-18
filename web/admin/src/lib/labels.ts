import type {
  FeedbackPriority,
  FeedbackSource,
  FeedbackStatus,
  FeedbackType,
  Role,
  ScanStatus,
  WebhookDeliveryStatus,
  WebhookEventType,
  WebhookStatus,
} from "@/lib/api"

export const STATUS_LABEL: Record<FeedbackStatus, string> = {
  open: "待处理",
  in_progress: "处理中",
  resolved: "已解决",
  spam: "垃圾",
}

/** Tailwind classes per status; keys match the --status-* tokens in index.css. */
export const STATUS_CLASS: Record<FeedbackStatus, string> = {
  open: "bg-status-open-bg text-status-open",
  in_progress: "bg-status-progress-bg text-status-progress",
  resolved: "bg-status-resolved-bg text-status-resolved",
  spam: "bg-status-spam-bg text-status-spam",
}

export const TYPE_LABEL: Record<FeedbackType, string> = {
  bug: "缺陷",
  suggestion: "建议",
  other: "其他",
}

/** Provenance, not a workflow label — the server sets it and nothing edits it. */
export const SOURCE_LABEL: Record<FeedbackSource, string> = {
  widget: "用户提交",
  internal: "内部反馈",
}

export const SOURCE_CLASS: Record<FeedbackSource, string> = {
  widget: "",
  internal: "bg-primary/10 text-primary",
}

export const PRIORITY_LABEL: Record<FeedbackPriority, string> = {
  none: "无",
  low: "低",
  medium: "中",
  high: "高",
  urgent: "紧急",
}

export const PRIORITY_CLASS: Record<FeedbackPriority, string> = {
  none: "text-muted-foreground",
  low: "text-prio-low",
  medium: "text-prio-medium",
  high: "text-prio-high",
  urgent: "text-prio-urgent",
}

/** Rank for the "优先级优先" sort; higher sorts first. */
export const PRIORITY_RANK: Record<FeedbackPriority, number> = {
  urgent: 4,
  high: 3,
  medium: 2,
  low: 1,
  none: 0,
}

export const ROLE_LABEL: Record<Role, string> = {
  owner: "所有者",
  admin: "管理员",
  agent: "处理人",
  viewer: "只读",
}

export const SCAN_LABEL: Record<ScanStatus, string> = {
  pending: "扫描中",
  clean: "已扫描",
  flagged: "已隔离",
  rejected: "已拒绝",
}

export const STATUSES: FeedbackStatus[] = ["open", "in_progress", "resolved", "spam"]
export const TYPES: FeedbackType[] = ["bug", "suggestion", "other"]
export const SOURCES: FeedbackSource[] = ["widget", "internal"]
export const PRIORITIES: FeedbackPriority[] = ["none", "low", "medium", "high", "urgent"]

export const WEBHOOK_STATUS_LABEL: Record<WebhookStatus, string> = {
  active: "启用",
  paused: "已暂停",
}

/** Order is presentation order in the create form. */
export const WEBHOOK_EVENT_TYPES: WebhookEventType[] = [
  "feedback.created.v1",
  "feedback.status_changed.v1",
  "feedback.assigned.v1",
  "feedback.note_added.v1",
]

export const WEBHOOK_EVENT_TYPE_LABEL: Record<WebhookEventType, string> = {
  "feedback.created.v1": "新反馈",
  "feedback.status_changed.v1": "状态变更",
  "feedback.assigned.v1": "分配负责人",
  "feedback.note_added.v1": "内部备注",
}

export const DELIVERY_STATUS_LABEL: Record<WebhookDeliveryStatus, string> = {
  pending: "等待中",
  succeeded: "成功",
  failed: "失败",
}

/**
 * Allowed status transitions, mirroring the server state machine
 * (open ⇄ in_progress → resolved → reopen; spam → open). Offering only legal
 * targets keeps the UI from posting a request the API will reject with 422.
 */
export const STATUS_TRANSITIONS: Record<FeedbackStatus, FeedbackStatus[]> = {
  open: ["in_progress", "resolved", "spam"],
  in_progress: ["open", "resolved", "spam"],
  resolved: ["open"],
  spam: ["open"],
}
