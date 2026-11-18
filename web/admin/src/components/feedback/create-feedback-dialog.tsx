import { useEffect, useState } from "react"
import { toast } from "sonner"
import { api, type FeedbackType, type Project } from "@/lib/api"
import { TYPE_LABEL, TYPES } from "@/lib/labels"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

/** Mirrors the server's message cap so the counter and the API agree. */
const MESSAGE_MAX = 10000

const NO_RATING = "none"

/**
 * Files feedback on behalf of the team. Everything created here is stored with
 * `source: internal` — the server decides that from the session, so this form
 * never sends it and no field here can forge a customer report.
 *
 * The project is explicit rather than inherited from the sidebar filter: the
 * inbox is often viewed unscoped ("全部项目"), and a report has to land
 * somewhere specific.
 */
export function CreateFeedbackDialog({
  open,
  onOpenChange,
  projects,
  projectId,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  projects: Project[]
  /** Currently selected project, used as the default when one is selected. */
  projectId?: string
  onCreated: () => void
}) {
  const [project, setProject] = useState("")
  const [type, setType] = useState<FeedbackType>("bug")
  const [message, setMessage] = useState("")
  const [rating, setRating] = useState<string>(NO_RATING)
  const [email, setEmail] = useState("")
  const [saving, setSaving] = useState(false)

  // Reset on each open so a cancelled draft does not reappear, and default to
  // the filtered project, or to the only one when the workspace has just one.
  useEffect(() => {
    if (!open) return
    setProject(projectId ?? (projects.length === 1 ? projects[0].ID : ""))
    setType("bug")
    setMessage("")
    setRating(NO_RATING)
    setEmail("")
    setSaving(false)
  }, [open, projectId, projects])

  const trimmed = message.trim()
  const emailInvalid = email.trim() !== "" && !email.includes("@")
  const canSubmit = !saving && project !== "" && trimmed !== "" && trimmed.length <= MESSAGE_MAX && !emailInvalid

  async function submit() {
    if (!canSubmit) return
    setSaving(true)
    try {
      await api.createInternalFeedback({
        project_id: project,
        type,
        message: trimmed,
        rating: rating === NO_RATING ? null : Number(rating),
        ...(email.trim() ? { contact: { email: email.trim() } } : {}),
      })
      toast.success("已创建内部反馈")
      onOpenChange(false)
      onCreated()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "创建失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !saving && onOpenChange(next)}>
      <DialogContent className="max-w-md" showCloseButton={!saving}>
        <DialogHeader>
          <DialogTitle>新建内部反馈</DialogTitle>
          <DialogDescription>
            这里创建的反馈会标记为「内部反馈」，与用户通过组件提交的反馈区分开。
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
          className="grid gap-3"
        >
          <div className="grid gap-1.5">
            <Label htmlFor="cf-project" className="text-muted-foreground text-xs">
              所属项目
            </Label>
            <Select value={project} onValueChange={setProject}>
              <SelectTrigger id="cf-project" className="w-full">
                <SelectValue placeholder="选择一个项目" />
              </SelectTrigger>
              <SelectContent>
                {projects.map((p) => (
                  <SelectItem key={p.ID} value={p.ID}>
                    {p.Name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="cf-type" className="text-muted-foreground text-xs">
                类型
              </Label>
              <Select value={type} onValueChange={(v) => setType(v as FeedbackType)}>
                <SelectTrigger id="cf-type" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {TYPES.map((t) => (
                    <SelectItem key={t} value={t}>
                      {TYPE_LABEL[t]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="cf-rating" className="text-muted-foreground text-xs">
                评分（可选）
              </Label>
              <Select value={rating} onValueChange={setRating}>
                <SelectTrigger id="cf-rating" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NO_RATING}>不评分</SelectItem>
                  {[1, 2, 3, 4, 5].map((n) => (
                    <SelectItem key={n} value={String(n)}>
                      {n} 星
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-1.5">
            <Label htmlFor="cf-message" className="text-muted-foreground text-xs">
              反馈内容
            </Label>
            <Textarea
              id="cf-message"
              autoFocus
              rows={5}
              value={message}
              maxLength={MESSAGE_MAX}
              onChange={(e) => setMessage(e.target.value)}
              placeholder="描述问题或建议，越具体越好。"
            />
            <span
              className={cn(
                "text-muted-foreground text-right text-[11px] tabular-nums",
                trimmed.length > MESSAGE_MAX && "text-prio-urgent",
              )}
            >
              {trimmed.length} / {MESSAGE_MAX}
            </span>
          </div>

          <div className="grid gap-1.5">
            <Label htmlFor="cf-email" className="text-muted-foreground text-xs">
              相关用户邮箱（可选）
            </Label>
            <Input
              id="cf-email"
              type="email"
              value={email}
              maxLength={320}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="代客户记录时填写"
              aria-invalid={emailInvalid}
            />
            {emailInvalid && <span className="text-prio-urgent text-[11px]">请填写有效的邮箱地址。</span>}
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" disabled={saving} onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {saving ? "创建中…" : "创建反馈"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
