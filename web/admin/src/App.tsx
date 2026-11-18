import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"
import {
  api,
  UnauthenticatedError,
  type FeedbackFilters,
  type FeedbackListItem,
  type FeedbackStatus,
  type Member,
  type Organization,
  type Overview,
  type Project,
  type SetupState,
} from "@/lib/api"
import { MetricsPanel } from "@/components/analytics/metrics-panel"
import { WebhookManager } from "@/components/automation/webhook-manager"
import { FeedbackList } from "@/components/feedback/feedback-list"
import { FeedbackSheet } from "@/components/feedback/feedback-sheet"
import { EmbedPage } from "@/components/settings/embed-page"
import { CommandPalette } from "@/components/shell/command-palette"
import { LoginCard } from "@/components/shell/login-card"
import { SetupChecklist } from "@/components/shell/setup-checklist"
import { TABS, TopBar, type View } from "@/components/shell/top-bar"
import { WorkspaceSidebar } from "@/components/shell/workspace-sidebar"
import { Skeleton } from "@/components/ui/skeleton"

export default function App() {
  const [member, setMember] = useState<Member | null>(null)
  const [booting, setBooting] = useState(true)
  const [organization, setOrganization] = useState<Organization | null>(null)
  const [members, setMembers] = useState<Member[]>([])
  const [projects, setProjects] = useState<Project[]>([])
  const [setup, setSetup] = useState<SetupState | null>(null)
  const [overview, setOverview] = useState<Overview | null>(null)
  const [detailId, setDetailId] = useState<string | null>(null)
  const [tab, setTab] = useState<View>("feedback")
  const [paletteOpen, setPaletteOpen] = useState(false)
  // Filters live here because the sidebar, the metrics panel and the list all
  // drive them; the list owns only its own view/sort/selection.
  const [filters, setFilters] = useState<FeedbackFilters>({})
  const [search, setSearch] = useState("")
  // Mirrors the rows the list has fetched, so the metrics and the team-load
  // card can derive from the same page without fetching it a second time.
  const [loadedItems, setLoadedItems] = useState<FeedbackListItem[]>([])
  // Bumped whenever a write lands, so the list refetches the current page.
  const [reloadToken, setReloadToken] = useState(0)

  useEffect(() => {
    void (async () => {
      try {
        const { member, organization } = await api.me()
        setMember(member)
        setOrganization(organization)
      } catch {
        // Not signed in — the login card handles it.
      } finally {
        setBooting(false)
      }
    })()
  }, [])

  // Scoped to the selected project so the tiles, the sidebar queue counts and
  // the facet counts describe the same rows the inbox is showing.
  const projectId = filters.project_id

  const loadWorkspace = useCallback(async () => {
    try {
      const [memberList, projectList, stats, setupState] = await Promise.all([
        api.members(),
        api.projects(),
        api.overview(projectId),
        api.setup(),
      ])
      setMembers(memberList.members)
      setProjects(projectList.projects)
      setOverview(stats)
      setSetup(setupState)
    } catch (e) {
      if (e instanceof UnauthenticatedError) {
        setMember(null)
        return
      }
      toast.error(e instanceof Error ? e.message : "加载工作区数据失败")
    }
  }, [projectId])

  useEffect(() => {
    if (member) void loadWorkspace()
  }, [member, loadWorkspace])

  const refreshAll = useCallback(() => {
    setReloadToken((n) => n + 1)
    void loadWorkspace()
  }, [loadWorkspace])

  async function signOut() {
    try {
      await api.logout()
    } finally {
      setMember(null)
      setOrganization(null)
      setMembers([])
      setProjects([])
      setOverview(null)
      setSetup(null)
    }
  }

  if (booting) {
    return (
      <div className="mx-auto grid max-w-[1400px] gap-3 p-6">
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-96 w-full" />
      </div>
    )
  }

  if (!member)
    return (
      <LoginCard
        onSignedIn={({ member, organization }) => {
          setMember(member)
          setOrganization(organization)
        }}
      />
    )

  const project = projects.find((p) => p.ID === filters.project_id)
  const canManage = member.Role === "owner" || member.Role === "admin"
  const openEmbed = () => setTab("embed")
  const openAutomation = () => setTab("automation")
  // One set of props, two renderers: the permanent column from `lg` up and the
  // topbar's drawer below it.
  const sidebar = {
    member,
    organization,
    members,
    projects,
    projectId: filters.project_id,
    onProjectChange: (id?: string) => setFilters((f) => ({ ...f, project_id: id })),
    onProjectsChanged: refreshAll,
    onOrganizationChanged: setOrganization,
    overview,
    status: filters.status,
    onStatusChange: (status?: FeedbackStatus) => setFilters((f) => ({ ...f, status })),
    onSignOut: signOut,
    view: tab,
    onOpenEmbed: openEmbed,
  }

  return (
    <div className="flex min-h-dvh">
      <WorkspaceSidebar {...sidebar} />

      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar
          member={member}
          organization={organization}
          members={members}
          project={project}
          feedbackCount={overview?.total}
          tab={tab}
          onTabChange={setTab}
          onOpenSearch={() => setPaletteOpen(true)}
          onOpenEmbed={openEmbed}
          sidebar={sidebar}
        />

        {tab === "feedback" ? (
          <main className="flex flex-col gap-4 px-5 py-5 pb-16">
            <SetupChecklist setup={setup} onOpenEmbed={openEmbed} onOpenAutomation={openAutomation} />
            <MetricsPanel
              overview={overview}
              items={loadedItems}
              onStatusChange={(status) => setFilters((f) => ({ ...f, status }))}
            />
            <FeedbackList
              members={members}
              currentMemberId={member.ID}
              overview={overview}
              filters={filters}
              onFiltersChange={setFilters}
              search={search}
              onSearchChange={setSearch}
              onOpenDetail={setDetailId}
              onDataChanged={refreshAll}
              onItemsLoaded={setLoadedItems}
              reloadToken={reloadToken}
              onOpenAutomation={openAutomation}
              onOpenEmbed={openEmbed}
              webhookConnected={(setup?.endpoint_count ?? 0) > 0}
              projects={projects}
            />
          </main>
        ) : tab === "automation" ? (
          <WebhookManager canManage={canManage} onDataChanged={refreshAll} />
        ) : tab === "embed" ? (
          <EmbedPage
            projects={projects}
            projectId={filters.project_id}
            canManage={canManage}
            onProjectsChanged={refreshAll}
            onOpenAutomation={openAutomation}
            webhookCount={setup?.endpoint_count ?? 0}
          />
        ) : (
          <main className="grid place-items-center gap-2 px-5 py-24 text-center">
            <span className="cap">{TABS.find((t) => t.key === tab)?.label}</span>
            <h2 className="text-lg font-semibold">这一块还没做</h2>
            <p className="text-muted-foreground max-w-[46ch] text-sm">
              导航先把位置留出来，接口和页面补上后就能用。现在可以先回「反馈」继续处理。
            </p>
          </main>
        )}
      </div>

      <FeedbackSheet
        feedbackId={detailId}
        members={members}
        onOpenChange={(open) => !open && setDetailId(null)}
        onSaved={refreshAll}
      />

      <CommandPalette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        items={loadedItems}
        onSelect={setDetailId}
        onSearch={setSearch}
      />
    </div>
  )
}
