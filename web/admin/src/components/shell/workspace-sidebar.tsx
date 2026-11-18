import { useState } from "react"
import { ChevronsUpDown, Copy, Inbox, KeyRound, LogOut, Pencil, Plug, Settings, Users } from "lucide-react"
import { toast } from "sonner"
import { api } from "@/lib/api"
import type { FeedbackStatus, Member, Organization, Overview, Project } from "@/lib/api"
import { avatarTint } from "@/lib/avatar"
import { initials } from "@/lib/format"
import { ROLE_LABEL, STATUS_LABEL } from "@/lib/labels"
import { NavGroup, NavItem } from "@/components/shell/nav-item"
import { comingSoon } from "@/components/shell/coming-soon"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

/** Environment badge; production is the one worth spotting before you act. */
export function EnvBadge({ environment }: { environment: string }) {
  const production = environment === "production" || environment === "prod"
  return (
    <span
      className={`rounded-[5px] px-1.5 py-px font-mono text-[9.5px] tracking-wider ${
        production
          ? "bg-status-resolved-bg text-status-resolved"
          : "bg-status-progress-bg text-status-progress"
      }`}
    >
      {production ? "生产" : environment}
    </span>
  )
}

const STATUS_DOT: Record<FeedbackStatus, string> = {
  open: "bg-status-open",
  in_progress: "bg-status-progress",
  resolved: "bg-status-resolved",
  spam: "bg-status-spam",
}

/**
 * The sidebar holds sign-out, the project switcher and the status queues, so
 * below `lg` — where the column itself is hidden — the same content has to be
 * reachable from the topbar's menu button. Both renderers share this body;
 * `onNavigate` lets the drawer close itself after a choice.
 */
export function WorkspaceSidebarContent({
  member,
  organization,
  members,
  projects,
  projectId,
  onProjectChange,
  onProjectsChanged,
  onOrganizationChanged,
  overview,
  status,
  onStatusChange,
  onSignOut,
  view,
  onOpenEmbed,
  onNavigate,
}: {
  member: Member
  organization: Organization | null
  members: Member[]
  projects: Project[]
  projectId?: string
  onProjectChange: (id?: string) => void
  /** Called after a project is renamed, so the workspace lists refetch. */
  onProjectsChanged: () => void
  /** Called with the updated organization after a rename. */
  onOrganizationChanged: (org: Organization) => void
  overview: Overview | null
  status?: FeedbackStatus
  onStatusChange: (status?: FeedbackStatus) => void
  onSignOut: () => void
  /** The page the content area is showing, so the workspace entries highlight. */
  view?: string
  /** Opens 嵌入与集成 (write key, install snippet, integrations). */
  onOpenEmbed?: () => void
  /** Called after any navigation choice, so the mobile drawer can close. */
  onNavigate?: () => void
}) {
  const statuses: FeedbackStatus[] = ["open", "in_progress", "resolved", "spam"]
  const name = member.Name || member.Email
  const navigate = <T,>(fn: (value: T) => void) => (value: T) => {
    fn(value)
    onNavigate?.()
  }
  const chooseProject = navigate(onProjectChange)
  const chooseStatus = navigate(onStatusChange)
  const openEmbed = navigate(() => onOpenEmbed?.())
  const canRename = member.Role === "owner" || member.Role === "admin"
  // An empty object means the organization itself is being renamed.
  const [renaming, setRenaming] = useState<{ project?: Project } | null>(null)
  const [draft, setDraft] = useState("")
  const [saving, setSaving] = useState(false)
  // New-project dialog state. `created` carries the write key, which the API
  // returns exactly once, so the dialog flips to a "copy it now" phase.
  const [creating, setCreating] = useState(false)
  const [projName, setProjName] = useState("")
  const [projEnv, setProjEnv] = useState("development")
  const [created, setCreated] = useState<{ name: string; writeKey: string } | null>(null)

  const submitCreate = async () => {
    const name = projName.trim()
    if (!name || saving) return
    setSaving(true)
    try {
      const p = await api.createProject(name, projEnv)
      setCreated({ name: p.Name, writeKey: p.WriteKey })
      onProjectsChanged()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "新建项目失败")
    } finally {
      setSaving(false)
    }
  }

  const closeCreate = () => {
    setCreating(false)
    setProjName("")
    setProjEnv("development")
    setCreated(null)
  }

  const copyWriteKey = async () => {
    if (!created) return
    try {
      await navigator.clipboard.writeText(created.writeKey)
      toast.success("写密钥已复制")
    } catch {
      toast.error("复制失败，请手动选择复制")
    }
  }

  const submitRename = async () => {
    const name = draft.trim()
    if (!renaming || !name || saving) return
    setSaving(true)
    try {
      if (renaming.project) {
        await api.renameProject(renaming.project.ID, name)
        onProjectsChanged()
      } else {
        onOrganizationChanged(await api.renameOrganization(name))
      }
      setRenaming(null)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "重命名失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full flex-col gap-4 overflow-y-auto p-3">
      {/* Titled by the organisation, with the counts under it; the menu
          switches the project the inbox is scoped to. */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            className="bg-panel-2 hover:bg-panel-3 focus-visible:ring-ring/50 flex w-full items-center gap-2.5 rounded-[11px] border p-2 text-left outline-none focus-visible:ring-[3px]"
          >
            <span className="from-primary to-brand-deep text-primary-foreground grid size-7 shrink-0 place-items-center rounded-[9px] bg-linear-160 text-[13px] font-bold">
              F
            </span>
            <span className="min-w-0 leading-tight">
              <span className="block truncate text-[13px] font-semibold tracking-tight">
                {organization?.Name ?? "工作区"}
              </span>
              <span className="text-muted-foreground block text-[11px]">
                {projects.length} 个项目 · {members.length} 位成员
              </span>
            </span>
            <ChevronsUpDown className="text-muted-foreground ml-auto size-3.5" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-[212px]">
          <DropdownMenuLabel>切换项目</DropdownMenuLabel>
          <DropdownMenuItem onSelect={() => chooseProject(undefined)}>全部项目</DropdownMenuItem>
          {projects.map((project) => (
            <DropdownMenuItem key={project.ID} onSelect={() => chooseProject(project.ID)}>
              <span className="truncate">{project.Name}</span>
              <EnvBadge environment={project.Environment} />
            </DropdownMenuItem>
          ))}
          {canRename && (
            <DropdownMenuItem
              onSelect={() => {
                setDraft(organization?.Name ?? "")
                setRenaming({})
              }}
            >
              <Pencil className="size-3.5" />
              重命名工作区
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <NavGroup
        title="项目"
        action={
          // Same gate as the rename pencil below: creating a project is
          // owner/admin-only server-side, so offering the button to an agent or
          // viewer only leads to an untranslated 403 from the API.
          canRename && (
            <Button
              variant="ghost"
              size="icon-xs"
              className="ml-auto"
              aria-label="新建项目"
              onClick={() => setCreating(true)}
            >
              ＋
            </Button>
          )
        }
      >
        {projects.map((project) => (
          <div key={project.ID} className="group/proj relative">
            <NavItem
              active={projectId === project.ID}
              onClick={() => chooseProject(projectId === project.ID ? undefined : project.ID)}
              trailing={<EnvBadge environment={project.Environment} />}
            >
              {project.Name}
            </NavItem>
            {canRename && (
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={`重命名 ${project.Name}`}
                className="bg-panel-2 absolute top-1/2 right-1.5 -translate-y-1/2 opacity-0 group-hover/proj:opacity-100 focus-visible:opacity-100"
                onClick={() => {
                  setDraft(project.Name)
                  setRenaming({ project })
                }}
              >
                <Pencil className="size-3" />
              </Button>
            )}
          </div>
        ))}
      </NavGroup>

      <NavGroup title="反馈">
        <NavItem
          icon={<Inbox className="size-3.5" />}
          active={!status}
          count={overview?.total}
          onClick={() => chooseStatus(undefined)}
        >
          收件箱
        </NavItem>
        {statuses.map((key) => (
          <NavItem
            key={key}
            icon={<span className={`size-1.5 rounded-[2px] ${STATUS_DOT[key]}`} />}
            active={status === key}
            count={overview?.by_status[key] ?? 0}
            onClick={() => chooseStatus(status === key ? undefined : key)}
          >
            {key === "spam" ? "垃圾信息" : STATUS_LABEL[key]}
          </NavItem>
        ))}
      </NavGroup>

      <NavGroup title="工作区">
        <NavItem icon={<Settings className="size-3.5" />} onClick={comingSoon}>
          项目设置
        </NavItem>
        <NavItem icon={<Plug className="size-3.5" />} active={view === "embed"} onClick={() => openEmbed(undefined)}>
          嵌入与集成
        </NavItem>
        <NavItem icon={<Users className="size-3.5" />} count={members.length} onClick={comingSoon}>
          成员与权限
        </NavItem>
        {/* The write key is the only key there is; it lives on 嵌入与集成. */}
        <NavItem icon={<KeyRound className="size-3.5" />} active={view === "embed"} onClick={() => openEmbed(undefined)}>
          API 与密钥
        </NavItem>
      </NavGroup>

      <div className="hover:bg-panel-2 mt-auto flex items-center gap-2.5 rounded-[11px] p-2">
        <Avatar className="size-7">
          {/* Google avatar URLs 403 when a referrer is sent. */}
          {member.Picture && <AvatarImage src={member.Picture} alt={name} referrerPolicy="no-referrer" />}
          <AvatarFallback className="avatar-tint font-mono text-[10.5px] font-bold" style={avatarTint(name)}>
            {initials(name)}
          </AvatarFallback>
        </Avatar>
        <span className="min-w-0 leading-tight">
          <span className="block truncate text-[12.5px] font-semibold">{name}</span>
          <span className="text-muted-foreground block text-[11px]">{ROLE_LABEL[member.Role]}</span>
        </span>
        <Button
          variant="ghost"
          size="icon-sm"
          className="ml-auto"
          onClick={onSignOut}
          aria-label="退出登录"
        >
          <LogOut />
        </Button>
      </div>

      <Dialog open={renaming !== null} onOpenChange={(open) => !open && setRenaming(null)}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{renaming?.project ? "重命名项目" : "重命名工作区"}</DialogTitle>
          </DialogHeader>
          <form
            onSubmit={(e) => {
              e.preventDefault()
              void submitRename()
            }}
            className="grid gap-3"
          >
            <Input
              autoFocus
              value={draft}
              maxLength={120}
              onChange={(e) => setDraft(e.target.value)}
              placeholder={renaming?.project ? "项目名称" : "工作区名称"}
            />
            <DialogFooter>
              <Button type="submit" disabled={saving || !draft.trim()}>
                保存
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={creating} onOpenChange={(open) => !open && closeCreate()}>
        {/*
          While `created` holds the write key, 完成 is the single exit: the
          key is also on 嵌入与集成 now, but the create flow should end with
          it copied, not with a stray Escape and a hunt for where it went.
        */}
        <DialogContent
          className="max-w-sm"
          showCloseButton={!created}
          onEscapeKeyDown={(e) => created && e.preventDefault()}
          onInteractOutside={(e) => created && e.preventDefault()}
        >
          <DialogHeader>
            <DialogTitle>{created ? "项目已创建" : "新建项目"}</DialogTitle>
          </DialogHeader>
          {created ? (
            <div className="grid gap-3">
              <p className="text-muted-foreground text-sm">
                「{created.name}」已创建。把下面的写密钥填进 SDK 就能开始接收反馈；之后也可以在「嵌入与集成」里查看。
              </p>
              <div className="bg-panel-2 flex items-center gap-2 rounded-md border p-2">
                <code className="min-w-0 flex-1 break-all font-mono text-xs">{created.writeKey}</code>
                <Button variant="outline" size="sm" className="h-7 shrink-0 rounded-md" onClick={copyWriteKey}>
                  <Copy className="size-3.5" /> 复制
                </Button>
              </div>
              <DialogFooter>
                <Button onClick={closeCreate}>完成</Button>
              </DialogFooter>
            </div>
          ) : (
            <form
              onSubmit={(e) => {
                e.preventDefault()
                void submitCreate()
              }}
              className="grid gap-3"
            >
              <Input
                autoFocus
                value={projName}
                maxLength={120}
                onChange={(e) => setProjName(e.target.value)}
                placeholder="项目名称"
              />
              <div className="grid gap-1.5">
                <span className="text-muted-foreground text-xs">环境</span>
                <Select value={projEnv} onValueChange={setProjEnv}>
                  <SelectTrigger className="w-full" size="sm">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="development">开发</SelectItem>
                    <SelectItem value="production">生产</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <DialogFooter>
                <Button type="submit" disabled={saving || !projName.trim()}>
                  {saving ? "创建中…" : "创建"}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** The permanent column, from `lg` up. Below that the drawer in TopBar owns it. */
export function WorkspaceSidebar(props: React.ComponentProps<typeof WorkspaceSidebarContent>) {
  return (
    <aside className="bg-card sticky top-0 hidden h-dvh w-[236px] shrink-0 border-r lg:block">
      <WorkspaceSidebarContent {...props} />
    </aside>
  )
}
