// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { WebhookEndpoint } from "@/lib/api"
import { WebhookManager } from "@/components/automation/webhook-manager"

beforeAll(() => {
  // Radix observes content size when the dialog mounts; jsdom has no layout.
  Element.prototype.scrollIntoView = vi.fn()
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

vi.mock("@/lib/api", async (importActual) => {
  const actual = await importActual<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      webhookEndpoints: vi.fn(),
      createWebhookEndpoint: vi.fn(),
      deleteWebhookEndpoint: vi.fn(),
      setWebhookEndpointStatus: vi.fn(),
      webhookDeliveries: vi.fn(),
      replayWebhookDelivery: vi.fn(),
    },
  }
})

const { api } = await import("@/lib/api")
const webhookEndpoints = vi.mocked(api.webhookEndpoints)
const createWebhookEndpoint = vi.mocked(api.createWebhookEndpoint)
const deleteWebhookEndpoint = vi.mocked(api.deleteWebhookEndpoint)
const setWebhookEndpointStatus = vi.mocked(api.setWebhookEndpointStatus)
const webhookDeliveries = vi.mocked(api.webhookDeliveries)
const replayWebhookDelivery = vi.mocked(api.replayWebhookDelivery)

const EP: WebhookEndpoint = {
  id: "wh1",
  url: "https://api.example.com/pinpost",
  event_types: ["feedback.created.v1"],
  status: "active",
  consecutive_failures: 0,
  created_at: "2026-09-05T00:00:00Z",
}

const DELIVERY = {
  id: "d1",
  event_id: "e1",
  event_type: "feedback.created.v1",
  attempt: 1,
  status: "succeeded",
  response_status: 200,
  response_body: "",
  latency_ms: 42,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
} as const

/** Clicks the header's 新建 Webhook (the empty state renders a second one). */
function openCreate() {
  fireEvent.click(screen.getAllByRole("button", { name: "新建 Webhook" })[0]!)
}

async function submit(url: string) {
  const input = await screen.findByPlaceholderText("https://api.example.com/pinpost")
  fireEvent.change(input, { target: { value: url } })
  fireEvent.click(screen.getByRole("button", { name: "创建" }))
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe("WebhookManager", () => {
  it("lists endpoints returned by the API", async () => {
    webhookEndpoints.mockResolvedValue({ endpoints: [EP] })
    render(<WebhookManager canManage />)

    await waitFor(() => expect(screen.getByText(EP.url)).toBeTruthy())
    expect(screen.getByText("启用")).toBeTruthy()
    expect(screen.getByText("新反馈")).toBeTruthy()
  })

  it("shows the signing secret once on create", async () => {
    webhookEndpoints.mockResolvedValue({ endpoints: [] })
    createWebhookEndpoint.mockResolvedValue({ ...EP, secret: "whsec_test" })
    render(<WebhookManager canManage />)
    await waitFor(() => expect(screen.getAllByRole("button", { name: "新建 Webhook" }).length).toBe(2))

    openCreate()
    await submit("https://api.example.com/hook")

    await waitFor(() => expect(screen.getByText("whsec_test")).toBeTruthy())
  })

  // The secret exists nowhere else once created — like the write key, a stray
  // Escape or overlay click is a permanent loss, so the dialog must refuse to
  // close while it is on screen.
  it("keeps the secret on screen when Escape is pressed", async () => {
    webhookEndpoints.mockResolvedValue({ endpoints: [] })
    createWebhookEndpoint.mockResolvedValue({ ...EP, secret: "whsec_test" })
    render(<WebhookManager canManage />)
    await waitFor(() => expect(screen.getAllByRole("button", { name: "新建 Webhook" }).length).toBe(2))

    openCreate()
    await submit("https://api.example.com/hook")
    await waitFor(() => expect(screen.getByText("whsec_test")).toBeTruthy())

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape", code: "Escape" })
    expect(screen.getByText("whsec_test")).toBeTruthy()
  })

  // Closing mid-flight would drop the one-time secret on the floor: the POST
  // is not cancelled, so it must land on a dialog that is still open.
  it("keeps the dialog open while the create is in flight", async () => {
    webhookEndpoints.mockResolvedValue({ endpoints: [] })
    let settle: (ep: WebhookEndpoint) => void = () => {}
    createWebhookEndpoint.mockReturnValue(
      new Promise<WebhookEndpoint>((resolve) => {
        settle = resolve
      }),
    )
    render(<WebhookManager canManage />)
    await waitFor(() => expect(screen.getAllByRole("button", { name: "新建 Webhook" }).length).toBe(2))

    openCreate()
    await submit("https://api.example.com/hook")
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape", code: "Escape" })

    settle({ ...EP, secret: "whsec_test" })
    await waitFor(() => expect(screen.getByText("whsec_test")).toBeTruthy())
  })

  it("pauses an active endpoint", async () => {
    webhookEndpoints.mockResolvedValue({ endpoints: [EP] })
    setWebhookEndpointStatus.mockResolvedValue({ status: "paused" })
    render(<WebhookManager canManage />)
    await waitFor(() => expect(screen.getByText(EP.url)).toBeTruthy())

    fireEvent.click(screen.getByLabelText("暂停"))
    await waitFor(() => expect(setWebhookEndpointStatus).toHaveBeenCalledWith("wh1", "paused"))
  })

  it("deletes after confirming", async () => {
    webhookEndpoints.mockResolvedValue({ endpoints: [EP] })
    deleteWebhookEndpoint.mockResolvedValue({ status: "deleted" })
    render(<WebhookManager canManage />)
    await waitFor(() => expect(screen.getByText(EP.url)).toBeTruthy())

    fireEvent.click(screen.getByLabelText("删除端点"))
    fireEvent.click(await screen.findByRole("button", { name: "删除" }))
    await waitFor(() => expect(deleteWebhookEndpoint).toHaveBeenCalledWith("wh1"))
  })

  // A viewer never fetches: every webhook route 403s below admin, so the page
  // must show the permission notice instead of a burst of untranslated errors.
  it("shows a permission notice to non-admins and never calls the API", () => {
    render(<WebhookManager canManage={false} />)

    expect(screen.getByText(/只有所有者和管理员/)).toBeTruthy()
    expect(webhookEndpoints).not.toHaveBeenCalled()
  })

  it("loads delivery history and offers replay", async () => {
    webhookEndpoints.mockResolvedValue({ endpoints: [EP] })
    webhookDeliveries.mockResolvedValue({
      deliveries: [
        {
          id: "d1",
          event_id: "e1",
          event_type: "feedback.created.v1",
          attempt: 1,
          status: "succeeded",
          response_status: 200,
          response_body: "",
          latency_ms: 42,
          created_at: "2026-09-05T00:00:00Z",
          updated_at: "2026-09-05T00:00:00Z",
        },
      ],
    })
    render(<WebhookManager canManage />)
    await waitFor(() => expect(screen.getByText(EP.url)).toBeTruthy())

    fireEvent.click(screen.getByRole("button", { name: /投递历史/ }))
    await waitFor(() => expect(screen.getByText("成功")).toBeTruthy())
    expect(screen.getByLabelText("重新投递")).toBeTruthy()
  })

  // The replay route is keyed by delivery id; passing the endpoint id 404s.
  it("replays a finished delivery and refreshes the row", async () => {
    webhookEndpoints.mockResolvedValue({ endpoints: [EP] })
    webhookDeliveries.mockResolvedValue({ deliveries: [DELIVERY] })
    replayWebhookDelivery.mockResolvedValue({ status: "queued" })
    render(<WebhookManager canManage />)
    await waitFor(() => expect(screen.getByText(EP.url)).toBeTruthy())

    fireEvent.click(screen.getByRole("button", { name: /投递历史/ }))
    fireEvent.click(await screen.findByLabelText("重新投递"))

    await waitFor(() => expect(replayWebhookDelivery).toHaveBeenCalledWith("d1"))
    await waitFor(() => expect(webhookDeliveries).toHaveBeenCalledTimes(2))
  })

  // A slower earlier request must not paint its rows under another endpoint.
  it("ignores a stale delivery response from a previously expanded endpoint", async () => {
    const other: WebhookEndpoint = { ...EP, id: "wh2", url: "https://api.example.com/other" }
    webhookEndpoints.mockResolvedValue({ endpoints: [EP, other] })
    let settleFirst: (r: { deliveries: typeof DELIVERY[] }) => void = () => {}
    webhookDeliveries
      .mockReturnValueOnce(new Promise((resolve) => (settleFirst = resolve)))
      .mockResolvedValueOnce({ deliveries: [{ ...DELIVERY, id: "d2", response_status: 500 }] })
    render(<WebhookManager canManage />)
    await waitFor(() => expect(screen.getByText(other.url)).toBeTruthy())

    const toggles = screen.getAllByRole("button", { name: /投递历史/ })
    fireEvent.click(toggles[0]!)
    fireEvent.click(toggles[1]!)
    await waitFor(() => expect(screen.getByText("500")).toBeTruthy())

    settleFirst({ deliveries: [DELIVERY] })
    await waitFor(() => expect(screen.getByText("500")).toBeTruthy())
    expect(screen.queryByText("200")).toBeNull()
  })
})
