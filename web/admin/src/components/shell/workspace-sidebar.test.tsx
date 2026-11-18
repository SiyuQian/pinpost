// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { Member, Organization, Project } from "@/lib/api"
import { WorkspaceSidebarContent } from "@/components/shell/workspace-sidebar"

/**
 * The create flow should end with the write key copied, so the two cases that
 * matter here are a stray Escape while the key is on screen, and the ＋ button
 * being offered to a role the API will refuse.
 */
vi.mock("@/lib/api", async (importActual) => {
  const actual = await importActual<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: { createProject: vi.fn(), renameProject: vi.fn(), renameOrganization: vi.fn() },
  }
})

const { api } = await import("@/lib/api")
const createProject = vi.mocked(api.createProject)

const ORG: Organization = { ID: "o1", Name: "Demo Org" }
const PROJECTS: Project[] = [{ ID: "p1", Name: "Demo Project", Environment: "development" }]

function member(role: Member["Role"]): Member {
  return { ID: "m1", Name: "Ada", Email: "ada@example.com", Role: role, OrganizationID: "o1" }
}

function renderSidebar(role: Member["Role"] = "owner") {
  return render(
    <WorkspaceSidebarContent
      member={member(role)}
      organization={ORG}
      members={[member(role)]}
      projects={PROJECTS}
      onProjectChange={() => {}}
      onProjectsChanged={() => {}}
      onOrganizationChanged={() => {}}
      overview={null}
      onStatusChange={() => {}}
      onSignOut={() => {}}
    />
  )
}

/** Opens the dialog, fills the name and submits, the way the sidebar is used. */
async function create(name: string) {
  fireEvent.click(screen.getByLabelText("新建项目"))
  const input = await screen.findByPlaceholderText("项目名称")
  fireEvent.change(input, { target: { value: name } })
  fireEvent.click(screen.getByRole("button", { name: "创建" }))
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe("WorkspaceSidebarContent create project", () => {
  it("shows the write key once the project is created", async () => {
    createProject.mockResolvedValue({
      ID: "p2",
      Name: "New Project",
      Environment: "development",
      WriteKey: "wk_test",
    })
    renderSidebar()
    await create("New Project")

    await waitFor(() => expect(screen.getByText("wk_test")).toBeTruthy())
  })

  // Escape, the overlay and the X all route to onOpenChange(false), which
  // clears `created` — the dialog must refuse to close while it is showing
  // a key, so the flow ends with it copied rather than with a hunt for it.
  it("keeps the write key on screen when Escape is pressed", async () => {
    createProject.mockResolvedValue({
      ID: "p2",
      Name: "New Project",
      Environment: "development",
      WriteKey: "wk_test",
    })
    renderSidebar()
    await create("New Project")
    await waitFor(() => expect(screen.getByText("wk_test")).toBeTruthy())

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape", code: "Escape" })
    expect(screen.getByText("wk_test")).toBeTruthy()

    // There is no X to close it either — 完成 is the only way out.
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull()
  })

  // The server refuses a viewer with 403 and request() surfaces the raw English
  // message, so an unguarded ＋ is a door that opens onto an untranslated error.
  it("hides the create button from roles the API will refuse", () => {
    renderSidebar("viewer")
    expect(screen.queryByLabelText("新建项目")).toBeNull()
  })

  it("offers the create button to admins", () => {
    renderSidebar("admin")
    expect(screen.getByLabelText("新建项目")).toBeTruthy()
  })
})
