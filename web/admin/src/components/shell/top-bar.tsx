import { useState } from "react"
import { Bell, HelpCircle, Menu, Search } from "lucide-react"
import type { Member, Organization, Project } from "@/lib/api"
import { avatarTint } from "@/lib/avatar"
import { initials } from "@/lib/format"
import { comingSoon } from "@/components/shell/coming-soon"
import { EnvBadge, WorkspaceSidebarContent } from "@/components/shell/workspace-sidebar"
import { ThemeToggle } from "@/components/shell/theme-toggle"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetDescription, SheetTitle, SheetTrigger } from "@/components/ui/sheet"
import { cn } from "@/lib/utils"

export const TABS = [
  { key: "feedback", label: "反馈" },
  { key: "insights", label: "洞察" },
  { key: "automation", label: "自动化", badge: "BETA" },
  { key: "widget", label: "组件外观" },
  { key: "settings", label: "设置" },
] as const

export type TabKey = (typeof TABS)[number]["key"]

/**
 * What the content area shows. The tabs above plus pages reached from the
 * sidebar or a button rather than the tab strip — `embed` is 嵌入与集成.
 */
export type View = TabKey | "embed"

/** Where the help button goes; the design doc and README live in the repo. */
export const HELP_URL = "https://github.com/SiyuQian/pinpost#readme"

/** Overlapping avatar stack: who else is in this workspace, in three glyphs. */
function MemberStack({ members }: { members: Member[] }) {
  const shown = members.slice(0, 3)
  const rest = members.length - shown.length
  return (
    <span className="hidden items-center pl-1.5 xl:flex" title={`${members.length} 位成员`}>
      {shown.map((m) => {
        const name = m.Name || m.Email
        return (
          <Avatar key={m.ID} className="ring-card -ml-1.5 size-[22px] ring-2">
            {m.Picture && <AvatarImage src={m.Picture} alt={name} referrerPolicy="no-referrer" />}
            <AvatarFallback
              className="avatar-tint font-mono text-[9.5px] font-bold"
              style={avatarTint(name)}
            >
              {initials(name)}
            </AvatarFallback>
          </Avatar>
        )
      })}
      {rest > 0 && (
        <span className="bg-panel-2 text-muted-foreground ring-card -ml-1.5 grid size-[22px] place-items-center rounded-full font-mono text-[9px] ring-2">
          +{rest}
        </span>
      )}
    </span>
  )
}

export function TopBar({
  member,
  organization,
  members,
  project,
  feedbackCount,
  tab,
  onTabChange,
  onOpenSearch,
  onOpenEmbed,
  sidebar,
}: {
  member: Member
  organization: Organization | null
  members: Member[]
  project?: Project
  feedbackCount?: number
  tab: View
  onTabChange: (tab: TabKey) => void
  onOpenSearch: () => void
  onOpenEmbed: () => void
  /**
   * The sidebar's props, rendered here as a drawer below `lg`. Sign-out, the
   * project switcher and the status queues live only in the sidebar, and
   * ending a session is not something a narrow window may lose.
   */
  sidebar: React.ComponentProps<typeof WorkspaceSidebarContent>
}) {
  const [navOpen, setNavOpen] = useState(false)

  return (
    <header className="bg-[var(--glass)] sticky top-0 z-20 border-b backdrop-blur-md backdrop-saturate-150">
      <div className="flex items-center gap-2.5 px-5 pt-2.5">
        <Sheet open={navOpen} onOpenChange={setNavOpen}>
          <SheetTrigger asChild>
            <Button variant="ghost" size="icon-sm" className="rounded-full lg:hidden" aria-label="打开导航">
              <Menu />
            </Button>
          </SheetTrigger>
          <SheetContent side="left" className="w-[260px] p-0">
            <SheetTitle className="sr-only">导航</SheetTitle>
            <SheetDescription className="sr-only">
              切换项目和状态队列，或退出登录。
            </SheetDescription>
            <WorkspaceSidebarContent {...sidebar} onNavigate={() => setNavOpen(false)} />
          </SheetContent>
        </Sheet>

        <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-[13px]">
          <span className="hidden truncate sm:inline">{organization?.Name ?? "工作区"}</span>
          <span className="hidden opacity-50 sm:inline">/</span>
          <span className="text-foreground truncate font-semibold">{project?.Name ?? "全部项目"}</span>
          {project && <EnvBadge environment={project.Environment} />}
        </div>

        <button
          type="button"
          onClick={onOpenSearch}
          className="bg-panel-2 text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 ml-auto flex h-8 min-w-0 items-center gap-2.5 rounded-full border pr-2.5 pl-3 text-[13px] outline-none focus-visible:ring-[3px] sm:min-w-[230px]"
        >
          <Search className="size-3.5" />
          <span className="hidden truncate sm:inline">搜索反馈、用户、标签…</span>
          <kbd className="text-muted-foreground ml-auto hidden rounded-md border px-1.5 font-mono text-[10px] sm:block">
            ⌘K
          </kbd>
        </button>

        {/* No unread dot: nothing produces notifications yet, and a dot that
            is always lit teaches people to ignore it. */}
        <Button variant="ghost" size="icon-sm" className="rounded-full" aria-label="通知" onClick={comingSoon}>
          <Bell />
        </Button>
        <Button variant="ghost" size="icon-sm" className="rounded-full" asChild>
          <a href={HELP_URL} target="_blank" rel="noreferrer" aria-label="帮助与文档">
            <HelpCircle />
          </a>
        </Button>
        <ThemeToggle />
        <MemberStack members={members.length ? members : [member]} />
        <Button variant="outline" size="sm" className="hidden rounded-full md:inline-flex" onClick={comingSoon}>
          邀请成员
        </Button>
        <Button size="sm" className="rounded-full" onClick={onOpenEmbed}>
          安装反馈组件
        </Button>
      </div>

      <nav className="flex gap-0.5 px-5 pt-2" aria-label="主导航">
        {TABS.map((item) => (
          <button
            key={item.key}
            type="button"
            onClick={() => onTabChange(item.key)}
            aria-current={tab === item.key ? "page" : undefined}
            className={cn(
              "focus-visible:ring-ring/50 rounded-t-md border-b-2 px-3 pt-1.5 pb-2.5 text-[13.5px] font-medium outline-none focus-visible:ring-[3px]",
              tab === item.key
                ? "border-primary text-foreground font-semibold"
                : "text-muted-foreground hover:text-foreground border-transparent",
            )}
          >
            {item.label}
            {item.key === "feedback" && feedbackCount !== undefined && (
              <span className="text-muted-foreground tnum ml-1.5 font-mono text-[10.5px]">
                {feedbackCount}
              </span>
            )}
            {"badge" in item && item.badge && (
              <span className="bg-status-progress-bg text-status-progress ml-1.5 rounded-[5px] px-1.5 py-px font-mono text-[9px] tracking-wider">
                {item.badge}
              </span>
            )}
          </button>
        ))}
      </nav>
    </header>
  )
}
