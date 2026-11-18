import type { Activity, Member } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import { PRIORITY_LABEL, STATUS_LABEL } from "@/lib/labels"
import type { FeedbackPriority, FeedbackStatus } from "@/lib/api"

function describe(activity: Activity, nameOf: (id?: string | null) => string) {
  const data = activity.data as Record<string, string | string[] | undefined>
  switch (activity.activity_type) {
    case "created":
      return "提交了这条反馈"
    case "status_changed":
      return `把状态从 ${STATUS_LABEL[data.from as FeedbackStatus] ?? data.from} 改为 ${
        STATUS_LABEL[data.to as FeedbackStatus] ?? data.to
      }`
    case "priority_changed":
      return `把优先级从 ${PRIORITY_LABEL[data.from as FeedbackPriority] ?? data.from} 改为 ${
        PRIORITY_LABEL[data.to as FeedbackPriority] ?? data.to
      }`
    case "assigned":
      return data.assignee_id ? `指派给 ${nameOf(data.assignee_id as string)}` : "取消了指派"
    case "tags_changed": {
      const tags = (data.tags as string[] | undefined) ?? []
      return tags.length ? `把标签设为 ${tags.join("、")}` : "清空了标签"
    }
    case "note_added":
      return `备注：${data.note}`
    default:
      return activity.activity_type
  }
}

export function ActivityTimeline({
  activities,
  members,
}: {
  activities: Activity[]
  members: Member[]
}) {
  const nameOf = (id?: string | null) => {
    if (!id) return "系统"
    const member = members.find((m) => m.ID === id)
    return member ? member.Name || member.Email : "成员"
  }

  const actorOf = (activity: Activity) => {
    if (activity.actor_type === "member") return nameOf(activity.actor_id)
    if (activity.actor_type === "end_user") return "提交者"
    return "系统"
  }

  if (activities.length === 0) {
    return <p className="text-muted-foreground text-sm">暂无活动记录</p>
  }

  return (
    <ol className="grid gap-0">
      {activities.map((activity, index) => (
        <li key={activity.id} className="grid grid-cols-[14px_1fr] gap-x-3">
          <div className="flex flex-col items-center">
            <span className="bg-border mt-1.5 size-1.5 shrink-0 rounded-full" aria-hidden />
            {index < activities.length - 1 && <span className="bg-border w-px flex-1" aria-hidden />}
          </div>
          <div className="pb-3 text-sm">
            <span className="font-medium">{actorOf(activity)}</span>{" "}
            <span className="text-muted-foreground">{describe(activity, nameOf)}</span>
            <div className="text-muted-foreground/70 tnum text-xs">{formatDateTime(activity.created_at)}</div>
          </div>
        </li>
      ))}
    </ol>
  )
}
