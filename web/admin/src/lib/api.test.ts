import { afterEach, expect, test, vi } from "vitest"
import { api } from "./api"

afterEach(() => vi.unstubAllGlobals())

function stubFetch(body: unknown, status = 200) {
  const fetch = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
  )
  vi.stubGlobal("fetch", fetch)
  return fetch
}

// The body key is PascalCase because the Go handler decodes into a struct;
// a drift to `credential` would authenticate nobody and fail silently in the UI.
test("googleLogin posts the PascalCase credential to /v1/auth/google", async () => {
  const fetch = stubFetch({ member: { ID: "m1" } })
  const { member } = await api.googleLogin("id-token")

  expect(member.ID).toBe("m1")
  const [path, init] = fetch.mock.calls[0]
  expect(path).toBe("/v1/auth/google")
  expect(init.method).toBe("POST")
  expect(JSON.parse(init.body)).toEqual({ Credential: "id-token" })
})

test("authConfig reads the client id", async () => {
  stubFetch({ google_client_id: "client-1" })
  expect((await api.authConfig()).google_client_id).toBe("client-1")
})

// An unconfigured deployment answers 404, and the card must see a rejection
// rather than a config object with an undefined id.
test("googleLogin surfaces the 404 from a deployment with no Google client", async () => {
  stubFetch({ error: { code: "google_disabled", message: "not configured" } }, 404)
  await expect(api.googleLogin("id-token")).rejects.toThrow("not configured")
})

// The webhook client is the only place these paths and snake_case bodies are
// written down; a rename would typecheck and 422 in production instead.
test("createWebhookEndpoint posts snake_case event_types to /v1/webhook-endpoints", async () => {
  const fetch = stubFetch({ id: "wh1", secret: "whsec_x" })
  await api.createWebhookEndpoint("https://api.example.com/hook", ["feedback.created.v1"])

  const [path, init] = fetch.mock.calls[0]
  expect(path).toBe("/v1/webhook-endpoints")
  expect(init.method).toBe("POST")
  expect(JSON.parse(init.body)).toEqual({
    url: "https://api.example.com/hook",
    event_types: ["feedback.created.v1"],
  })
})

test("setWebhookEndpointStatus patches /v1/webhook-endpoints/:id with {status}", async () => {
  const fetch = stubFetch({ status: "paused" })
  await api.setWebhookEndpointStatus("wh1", "paused")

  const [path, init] = fetch.mock.calls[0]
  expect(path).toBe("/v1/webhook-endpoints/wh1")
  expect(init.method).toBe("PATCH")
  expect(JSON.parse(init.body)).toEqual({ status: "paused" })
})

test("replayWebhookDelivery posts to the delivery id, not the endpoint id", async () => {
  const fetch = stubFetch({ status: "queued" })
  await api.replayWebhookDelivery("d1")

  const [path, init] = fetch.mock.calls[0]
  expect(path).toBe("/v1/webhook-deliveries/d1/replay")
  expect(init.method).toBe("POST")
})
