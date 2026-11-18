// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { Project } from "@/lib/api"
import { CreateFeedbackDialog } from "@/components/feedback/create-feedback-dialog"

/**
 * The dialog's job is to reach the dashboard-only endpoint with a project the
 * user actually chose, and to never let the form claim its own provenance —
 * `internal` is the server's word, not the client's. Submit gating matters
 * because a blank message would 422 after the dialog had already closed.
 */
beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

vi.mock("@/lib/api", async (importActual) => {
  const actual = await importActual<typeof import("@/lib/api")>()
  return { ...actual, api: { createInternalFeedback: vi.fn() } }
})

const { api } = await import("@/lib/api")
const createInternalFeedback = vi.mocked(api.createInternalFeedback)

const PROJECTS: Project[] = [
  { ID: "p1", Name: "Default", Environment: "production" },
  { ID: "p2", Name: "Staging", Environment: "development" },
]

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

function open(projectId?: string, onCreated = () => {}) {
  return render(
    <CreateFeedbackDialog
      open
      onOpenChange={() => {}}
      projects={PROJECTS}
      projectId={projectId}
      onCreated={onCreated}
    />,
  )
}

function submitButton() {
  return screen.getByRole("button", { name: "创建反馈" }) as HTMLButtonElement
}

describe("CreateFeedbackDialog", () => {
  it("submits the selected project and never sends a source", async () => {
    createInternalFeedback.mockResolvedValue({ feedback_id: "f1", created_at: "2026-09-05T00:00:00Z" })
    const onCreated = vi.fn()
    open("p2", onCreated)

    fireEvent.change(screen.getByLabelText("反馈内容"), { target: { value: "  后台导出很慢  " } })
    fireEvent.click(submitButton())

    await waitFor(() => expect(createInternalFeedback).toHaveBeenCalledTimes(1))
    const payload = createInternalFeedback.mock.calls[0][0]
    expect(payload.project_id).toBe("p2")
    // Trimmed, because the server rejects a whitespace-only message and the
    // stored copy should not carry the padding either.
    expect(payload.message).toBe("后台导出很慢")
    expect(payload).not.toHaveProperty("source")
    // No email typed means no contact object at all, rather than an empty one.
    expect(payload).not.toHaveProperty("contact")
    await waitFor(() => expect(onCreated).toHaveBeenCalled())
  })

  it("blocks submission until a message and a project are present", () => {
    // No project filter and more than one project: nothing is preselected, so
    // an eager submit would post a blank project_id.
    open()
    expect(submitButton().disabled).toBe(true)

    fireEvent.change(screen.getByLabelText("反馈内容"), { target: { value: "   " } })
    expect(submitButton().disabled).toBe(true)
    expect(createInternalFeedback).not.toHaveBeenCalled()
  })

  it("rejects a malformed contact email before posting", () => {
    open("p1")
    fireEvent.change(screen.getByLabelText("反馈内容"), { target: { value: "客户电话反馈" } })
    fireEvent.change(screen.getByLabelText("相关用户邮箱（可选）"), { target: { value: "not-an-email" } })

    expect(submitButton().disabled).toBe(true)
    expect(screen.getByText("请填写有效的邮箱地址。")).toBeTruthy()
  })
})
