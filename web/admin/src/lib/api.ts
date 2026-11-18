/**
 * Typed client for the Pinpost dashboard API (api/openapi.yaml).
 *
 * Note the casing split: workflow resources use snake_case, while the
 * auth/member/project payloads serialise Go structs directly and are
 * PascalCase. Both are mirrored faithfully here rather than normalised, so
 * the types stay checkable against the spec.
 */

export type FeedbackType = "bug" | "suggestion" | "other"
export type FeedbackStatus = "open" | "in_progress" | "resolved" | "spam"
export type FeedbackPriority = "none" | "low" | "medium" | "high" | "urgent"
export type Role = "owner" | "admin" | "agent" | "viewer"
/**
 * Who filed the report. `widget` is everything the public ingestion endpoint
 * accepted; `internal` is one a member filed from the dashboard. The server
 * decides it from the authenticated caller and no update path changes it, so
 * it is safe to render as provenance rather than as an editable label.
 */
export type FeedbackSource = "widget" | "internal"
export type ScanStatus = "pending" | "clean" | "flagged" | "rejected"

export interface FeedbackListItem {
  id: string
  type: FeedbackType
  message: string
  /** Submitter's address, when they left one; null for anonymous reports. */
  contact_email?: string | null
  /**
   * Page the report came from, with the query string, fragment and userinfo
   * removed; scheme, host and path are kept verbatim. Both the SDK and ingest
   * strip them, so the guarantee holds for direct posts too.
   */
  url?: string | null
  user_agent?: string | null
  source: FeedbackSource
  rating?: number | null
  status: FeedbackStatus
  priority: FeedbackPriority
  assignee_id?: string | null
  tags: string[]
  project_id: string
  created_at: string
  updated_at: string
}

export interface Attachment {
  id: string
  original_name: string
  mime_type: string
  size: number
  scan_status: ScanStatus
}

export interface FeedbackDetail extends FeedbackListItem {
  external_user_id?: string | null
  /** The full submission context; list rows carry only the projection above. */
  context: Record<string, unknown>
  attachments: Attachment[]
}

export interface Activity {
  id: string
  actor_type: "member" | "system" | "end_user"
  actor_id?: string | null
  activity_type: string
  data: Record<string, unknown>
  created_at: string
}

export interface Member {
  ID: string
  Name: string
  Email: string
  Role: Role
  OrganizationID: string
  Picture?: string
}

export interface Project {
  ID: string
  Name: string
  Environment: string
  /**
   * Public ingestion key, as pasted into the SDK. Missing on projects created
   * before the server stored it readable; one rotation fills it in.
   */
  WriteKey?: string
}

export interface Organization {
  ID: string
  Name: string
}

export type WebhookStatus = "active" | "paused"
export type WebhookEventType =
  | "feedback.created.v1"
  | "feedback.status_changed.v1"
  | "feedback.assigned.v1"
  | "feedback.note_added.v1"
export type WebhookDeliveryStatus = "pending" | "succeeded" | "failed"

export interface WebhookEndpoint {
  id: string
  url: string
  event_types: WebhookEventType[]
  status: WebhookStatus
  consecutive_failures: number
  created_at: string
  /** Present only in the create response, never again — like a write key. */
  secret?: string
}

export interface WebhookDelivery {
  id: string
  event_id: string
  event_type: WebhookEventType
  attempt: number
  status: WebhookDeliveryStatus
  next_attempt_at?: string | null
  response_status?: number | null
  response_body: string
  latency_ms?: number | null
  created_at: string
  updated_at: string
}

/**
 * Onboarding state, derived server-side from what the workspace holds. Steps
 * the API cannot prove (widget appearance, default assignee) are absent rather
 * than reported as undone.
 */
export interface SetupState {
  install_widget: boolean
  invite_members: boolean
  connect_notifications: boolean
  feedback_total: number
  member_count: number
  endpoint_count: number
}

export interface Overview {
  total: number
  by_status: Record<string, number>
  by_type: Record<string, number>
  trend: { day: string; count: number }[]
}

export interface FeedbackPage {
  items: FeedbackListItem[]
  next_cursor: string
}

export interface FeedbackFilters {
  status?: FeedbackStatus
  type?: FeedbackType
  source?: FeedbackSource
  assignee_id?: string
  project_id?: string
  q?: string
}

/** Thrown for any non-2xx response; `code` is the API's stable machine code. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
  }

  /** A stale updated_at lost the optimistic-concurrency race. */
  get isConflict() {
    return this.status === 409
  }
}

/** Raised on 401 so callers can drop back to the login screen. */
export class UnauthenticatedError extends ApiError {
  constructor(message = "会话已过期，请重新登录") {
    super(401, "unauthenticated", message)
    this.name = "UnauthenticatedError"
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: init.body ? { "Content-Type": "application/json", ...init.headers } : init.headers,
  })

  if (res.status === 204) return undefined as T

  const body = await res.json().catch(() => null)
  if (res.ok) return body as T

  const code = body?.error?.code ?? "unknown"
  const message = body?.error?.message ?? res.statusText
  if (res.status === 401) throw new UnauthenticatedError(message)
  throw new ApiError(res.status, code, message)
}

function query(params: Record<string, string | undefined>) {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value) search.set(key, value)
  }
  const s = search.toString()
  return s ? `?${s}` : ""
}

export const api = {
  me: () => request<{ member: Member; organization: Organization }>("/v1/me"),

  login: (email: string, password: string) =>
    request<{ member: Member; organization: Organization }>("/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ Email: email, Password: password }),
    }),

  /** Public sign-in configuration; an empty client id means Google is off. */
  authConfig: () => request<{ google_client_id: string }>("/v1/auth/config"),

  googleLogin: (credential: string) =>
    request<{ member: Member; organization: Organization }>("/v1/auth/google", {
      method: "POST",
      body: JSON.stringify({ Credential: credential }),
    }),

  logout: () => request<void>("/v1/auth/logout", { method: "POST" }),

  members: () => request<{ members: Member[] }>("/v1/members"),

  setup: () => request<SetupState>("/v1/setup"),

  projects: () => request<{ projects: Project[] }>("/v1/projects"),

  renameOrganization: (name: string) =>
    request<Organization>("/v1/organization", {
      method: "PATCH",
      body: JSON.stringify({ Name: name }),
    }),

  renameProject: (id: string, name: string) =>
    request<Project>(`/v1/projects/${id}`, {
      method: "PATCH",
      body: JSON.stringify({ Name: name }),
    }),

  /** Creates a project. The write key comes back here and on every listing. */
  createProject: (name: string, environment?: string) =>
    request<Project & { WriteKey: string }>("/v1/projects", {
      method: "POST",
      body: JSON.stringify({ Name: name, Environment: environment }),
    }),

  /**
   * Issues a new write key and revokes the old one at once. Every SDK still
   * holding the old key is refused from then on. Owner/admin only.
   */
  rotateWriteKey: (id: string) =>
    request<Project & { WriteKey: string }>(`/v1/projects/${id}/write-key/rotate`, {
      method: "POST",
    }),

  /** Pass a project to scope every count to it, matching a project-scoped list. */
  overview: (projectId?: string) =>
    request<Overview>("/v1/analytics/overview" + query({ project_id: projectId })),

  listFeedback: (filters: FeedbackFilters, cursor?: string, limit = 50) =>
    request<FeedbackPage>(
      "/v1/feedback" + query({ ...filters, cursor, limit: String(limit) }),
    ),

  /**
   * Files a report from the dashboard; the server stamps it `source:
   * internal`. The idempotency key makes a double-submitted form return the
   * first record instead of creating a second one.
   */
  createInternalFeedback: (input: {
    project_id: string
    type: FeedbackType
    message: string
    rating?: number | null
    contact?: { email: string }
  }) =>
    request<{ feedback_id: string; created_at: string }>("/v1/internal-feedback", {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify(input),
    }),

  getFeedback: (id: string) => request<FeedbackDetail>(`/v1/feedback/${id}`),

  activities: (id: string) => request<{ activities: Activity[] }>(`/v1/feedback/${id}/activities`),

  /**
   * `updated_at` is the optimistic-concurrency token — pass the value from the
   * record you read, and a concurrent edit surfaces as a 409 rather than a
   * silent overwrite.
   */
  updateFeedback: (
    id: string,
    patch: {
      status?: FeedbackStatus
      priority?: FeedbackPriority
      assignee_id?: string
      tags?: string[]
      updated_at?: string
    },
  ) => request<FeedbackDetail>(`/v1/feedback/${id}`, { method: "PATCH", body: JSON.stringify(patch) }),

  addNote: (id: string, note: string) =>
    request<void>(`/v1/feedback/${id}/notes`, { method: "POST", body: JSON.stringify({ Note: note }) }),

  attachmentUrl: (id: string) => `/v1/attachments/${id}/download`,

  /** Webhook endpoints; owner/admin only — every method here 403s otherwise. */
  webhookEndpoints: () => request<{ endpoints: WebhookEndpoint[] }>("/v1/webhook-endpoints"),

  /** The signing secret comes back once, in the create response only. */
  createWebhookEndpoint: (url: string, eventTypes: WebhookEventType[]) =>
    request<WebhookEndpoint>("/v1/webhook-endpoints", {
      method: "POST",
      body: JSON.stringify({ url, event_types: eventTypes }),
    }),

  deleteWebhookEndpoint: (id: string) =>
    request<{ status: string }>(`/v1/webhook-endpoints/${id}`, { method: "DELETE" }),

  setWebhookEndpointStatus: (id: string, status: WebhookStatus) =>
    request<{ status: string }>(`/v1/webhook-endpoints/${id}`, {
      method: "PATCH",
      body: JSON.stringify({ status }),
    }),

  webhookDeliveries: (id: string) =>
    request<{ deliveries: WebhookDelivery[] }>(`/v1/webhook-endpoints/${id}/deliveries`),

  replayWebhookDelivery: (id: string) =>
    request<{ status: string }>(`/v1/webhook-deliveries/${id}/replay`, { method: "POST" }),
}
