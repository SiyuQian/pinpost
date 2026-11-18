// Package api implements the generated ServerInterface (see gen.go, produced
// by oapi-codegen from api/openapi.yaml) by delegating to the domain slices.
// Routing and parameter parsing are generated; only auth + delegation live here.
//
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.6.0 -config ../../api/oapi-codegen.yaml ../../api/openapi.yaml
package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/SiyuQian/pinpost/internal/attachment"
	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/SiyuQian/pinpost/internal/feedback"
	"github.com/SiyuQian/pinpost/internal/outbox"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/project"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

type Server struct {
	Auth          *auth.Service
	Project       *project.Service
	Feedback      *feedback.Service
	Attachment    *attachment.Service
	Outbox        *outbox.Service
	IngestLimiter *platform.RateLimiter
	Google        auth.GoogleConfig
}

var _ ServerInterface = (*Server)(nil)

// Handler mounts all spec operations onto mux and returns it.
func (s *Server) Handler(mux *http.ServeMux) http.Handler {
	return HandlerWithOptions(s, StdHTTPServerOptions{
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			platform.WriteError(w, r, platform.Errf(400, "invalid_request", err.Error()))
		},
	})
}

// ---- shared helpers ----

func readJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(v); err != nil {
		return platform.Errf(400, "invalid_json", "Request body is not valid JSON")
	}
	return nil
}

// withProject authenticates the write key and applies ingestion rate limiting.
func (s *Server) withProject(w http.ResponseWriter, r *http.Request, fn func(p *auth.ProjectCtx) error) {
	err := func() error {
		key := r.Header.Get("X-Pinpost-Write-Key")
		if key == "" {
			key = r.URL.Query().Get("write_key")
		}
		p, err := s.Auth.ProjectByWriteKey(r.Context(), key)
		if err != nil {
			return err
		}
		if !s.IngestLimiter.Allow(p.ProjectID + "|" + platform.ClientIP(r)) {
			w.Header().Set("Retry-After", "30")
			return platform.Errf(429, "rate_limited", "Too many requests")
		}
		return fn(p)
	}()
	if err != nil {
		platform.WriteError(w, r, err)
	}
}

// withMember authenticates the dashboard session cookie.
func (s *Server) withMember(w http.ResponseWriter, r *http.Request, fn func(m *auth.Member) error) {
	err := func() error {
		m, err := s.Auth.MemberFromRequest(r)
		if err != nil {
			return err
		}
		return fn(m)
	}()
	if err != nil {
		platform.WriteError(w, r, err)
	}
}

func requireWrite(m *auth.Member) error {
	if !m.CanWrite() {
		return platform.Errf(403, "forbidden", "Your role cannot modify feedback")
	}
	return nil
}

// ---- ingestion ----

func (s *Server) GetSdkConfig(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(p *auth.ProjectCtx) error {
		platform.WriteJSON(w, 200, map[string]any{
			"environment": p.Environment,
			"settings":    json.RawMessage(p.Settings),
		})
		return nil
	})
}

func (s *Server) CreateAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(p *auth.ProjectCtx) error {
		var in attachment.CreateUploadInput
		if err := readJSON(r, &in); err != nil {
			return err
		}
		out, err := s.Attachment.CreateUpload(r.Context(), p.OrganizationID, p.ProjectID, &in)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 201, out)
		return nil
	})
}

func (s *Server) CreateFeedback(w http.ResponseWriter, r *http.Request, params CreateFeedbackParams) {
	s.withProject(w, r, func(p *auth.ProjectCtx) error {
		var in feedback.CreateInput
		if err := readJSON(r, &in); err != nil {
			return err
		}
		in.ProjectID = p.ProjectID
		in.OrganizationID = p.OrganizationID
		if params.IdempotencyKey != "" {
			in.IdempotencyKey = params.IdempotencyKey
		}
		out, err := s.Feedback.Create(r.Context(), &in)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 201, out)
		return nil
	})
}

// CreateInternalFeedback files a report on behalf of the signed-in member.
// Ingestion's write-key path stays untouched: this is the dashboard's own
// entry point, so the source is fixed to internal from the authenticated
// caller rather than taken from the body, and the project is checked to belong
// to the member's organization before anything is written.
func (s *Server) CreateInternalFeedback(w http.ResponseWriter, r *http.Request, params CreateInternalFeedbackParams) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireWrite(m); err != nil {
			return err
		}
		var body CreateInternalFeedbackJSONRequestBody
		if err := readJSON(r, &body); err != nil {
			return err
		}
		projectID := body.ProjectId.String()
		// Without this the record would land in the caller's own partition
		// carrying a project id from somewhere else — no cross-tenant read,
		// but a row that no project view can account for.
		projects, err := s.Project.ListProjects(r.Context(), m.OrganizationID)
		if err != nil {
			return err
		}
		owned := false
		for _, p := range projects {
			if p.ID == projectID {
				owned = true
				break
			}
		}
		if !owned {
			return platform.ValidationErr(map[string]string{"project_id": "unknown project"})
		}

		in := feedback.CreateInput{
			ProjectID:      projectID,
			OrganizationID: m.OrganizationID,
			IdempotencyKey: params.IdempotencyKey,
			Type:           string(body.Type),
			Message:        body.Message,
			Rating:         body.Rating,
			Source:         feedback.SourceInternal,
			ActorID:        m.ID,
		}
		if body.Contact != nil && body.Contact.Email != nil {
			in.Contact.Email = *body.Contact.Email
		}
		out, err := s.Feedback.Create(r.Context(), &in)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 201, out)
		return nil
	})
}

// ---- auth ----

func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	err := func() error {
		var in struct{ Email, Password string }
		if err := readJSON(r, &in); err != nil {
			return err
		}
		token, m, err := s.Auth.Login(r.Context(), in.Email, in.Password)
		if err != nil {
			return err
		}
		// The session payload is built before the cookie is set: everything
		// after SetSessionCookie writes a response the browser is already
		// authenticated by, so a failure there would show 登录失败 to someone
		// who is, in fact, signed in.
		body, err := s.session(r.Context(), m)
		if err != nil {
			return err
		}
		auth.SetSessionCookie(w, r, token)
		platform.WriteJSON(w, 200, body)
		return nil
	}()
	if err != nil {
		platform.WriteError(w, r, err)
	}
}

// session is the response both sign-in routes and /v1/me answer with: the
// member plus the organization whose workspace is being opened, so the admin
// app never needs a follow-up request for the name. It returns the body rather
// than writing it, so callers keep control of what happens before the write —
// in particular, of setting the session cookie only once nothing can fail.
func (s *Server) session(ctx context.Context, m *auth.Member) (map[string]any, error) {
	org, err := s.Project.Organization(ctx, m.OrganizationID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"member": m, "organization": org}, nil
}

// GetAuthConfig tells the admin app which sign-in methods to offer.
func (s *Server) GetAuthConfig(w http.ResponseWriter, r *http.Request) {
	id := ""
	if s.Google.Enabled() {
		id = s.Google.ClientID
	}
	platform.WriteJSON(w, 200, map[string]string{"google_client_id": id})
}

// GoogleLogin trades a Google ID token for the same session cookie the password
// path issues; who may sign in is the allowlist in s.Google, not a member row.
func (s *Server) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	err := func() error {
		var in struct{ Credential string }
		if err := readJSON(r, &in); err != nil {
			return err
		}
		token, m, err := s.Auth.LoginWithGoogle(r.Context(), s.Google, in.Credential)
		if err != nil {
			return err
		}
		body, err := s.session(r.Context(), m)
		if err != nil {
			return err
		}
		auth.SetSessionCookie(w, r, token)
		platform.WriteJSON(w, 200, body)
		return nil
	}()
	if err != nil {
		platform.WriteError(w, r, err)
	}
}

func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Logout(r.Context(), auth.SessionToken(r))
	auth.ClearSessionCookie(w, r)
	platform.WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	s.withMember(w, r, func(m *auth.Member) error {
		body, err := s.session(r.Context(), m)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, body)
		return nil
	})
}

// GetSetupState answers "what is left to set up" from what the workspace
// actually contains — feedback received, members invited, a webhook wired —
// rather than from flags a UI could tick on its own. Steps with nothing to
// read (widget appearance, default assignee) are deliberately absent until
// there is a record that proves them.
func (s *Server) GetSetupState(w http.ResponseWriter, r *http.Request) {
	s.withMember(w, r, func(m *auth.Member) error {
		overview, err := s.Feedback.Overview(r.Context(), m.OrganizationID, "")
		if err != nil {
			return err
		}
		members, err := s.Project.ListMembers(r.Context(), m.OrganizationID)
		if err != nil {
			return err
		}
		endpoints, err := s.Outbox.ListEndpoints(r.Context(), m.OrganizationID)
		if err != nil {
			return err
		}
		active := 0
		for _, e := range endpoints {
			if e.Status == "active" {
				active++
			}
		}
		platform.WriteJSON(w, 200, map[string]any{
			"install_widget":        overview.Total > 0,
			"invite_members":        len(members) > 1,
			"connect_notifications": active > 0,
			"feedback_total":        overview.Total,
			"member_count":          len(members),
			"endpoint_count":        active,
		})
		return nil
	})
}

// ---- project ----

func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request) {
	s.withMember(w, r, func(m *auth.Member) error {
		out, err := s.Project.ListProjects(r.Context(), m.OrganizationID)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, map[string]any{"projects": out})
		return nil
	})
}

func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		var in CreateProjectJSONBody
		if err := readJSON(r, &in); err != nil {
			return err
		}
		name := strings.TrimSpace(in.Name)
		if name == "" || utf8.RuneCountInString(name) > 120 {
			return platform.Errf(422, "invalid_name", "Project name must be 1–120 characters")
		}
		env := ""
		if in.Environment != nil {
			// Allowlisted, not just length-checked: EnvBadge styles on the exact
			// strings "production"/"prod", so any other value renders a project
			// labelled Production in the calm non-production colour — the one
			// mistake that badge exists to prevent. The values match the two the
			// console offers; this endpoint is reachable without it.
			env = strings.TrimSpace(string(*in.Environment))
			switch env {
			case "", "development", "production":
			default:
				return platform.Errf(422, "invalid_environment",
					"Environment must be development or production")
			}
		}
		p, writeKey, err := s.Project.Create(r.Context(), m.OrganizationID, name, env)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 201, map[string]any{
			"ID": p.ID, "Name": p.Name, "Environment": p.Environment, "WriteKey": writeKey,
		})
		return nil
	})
}

func (s *Server) UpdateOrganization(w http.ResponseWriter, r *http.Request) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		var in UpdateOrganizationJSONBody
		if err := readJSON(r, &in); err != nil {
			return err
		}
		name := strings.TrimSpace(in.Name)
		if name == "" || utf8.RuneCountInString(name) > 120 {
			return platform.Errf(422, "invalid_name", "Organization name must be 1–120 characters")
		}
		org, err := s.Project.RenameOrg(r.Context(), m.OrganizationID, name)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, org)
		return nil
	})
}

func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		var in UpdateProjectJSONBody
		if err := readJSON(r, &in); err != nil {
			return err
		}
		name := strings.TrimSpace(in.Name)
		if name == "" || utf8.RuneCountInString(name) > 120 {
			return platform.Errf(422, "invalid_name", "Project name must be 1–120 characters")
		}
		p, err := s.Project.Rename(r.Context(), m.OrganizationID, id.String(), name)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, p)
		return nil
	})
}

func (s *Server) RotateProjectWriteKey(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		p, err := s.Project.RotateWriteKey(r.Context(), m.OrganizationID, id.String())
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, p)
		return nil
	})
}

func (s *Server) ListMembers(w http.ResponseWriter, r *http.Request) {
	s.withMember(w, r, func(m *auth.Member) error {
		out, err := s.Project.ListMembers(r.Context(), m.OrganizationID)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, map[string]any{"members": out})
		return nil
	})
}

// ---- feedback dashboard ----

func (s *Server) ListFeedback(w http.ResponseWriter, r *http.Request, params ListFeedbackParams) {
	s.withMember(w, r, func(m *auth.Member) error {
		f := feedback.ListFilter{OrganizationID: m.OrganizationID}
		if params.ProjectId != nil {
			f.ProjectID = params.ProjectId.String()
		}
		if params.Status != nil {
			f.Status = string(*params.Status)
		}
		if params.Type != nil {
			f.Type = string(*params.Type)
		}
		if params.Source != nil {
			f.Source = string(*params.Source)
		}
		if params.AssigneeId != nil {
			f.AssigneeID = params.AssigneeId.String()
		}
		if params.Q != nil {
			f.Query = *params.Q
		}
		if params.Cursor != nil {
			f.Cursor = *params.Cursor
		}
		if params.Limit != nil {
			f.Limit = *params.Limit
		}
		items, next, err := s.Feedback.List(r.Context(), f)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
		return nil
	})
}

func (s *Server) GetFeedback(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		d, err := s.Feedback.Get(r.Context(), m.OrganizationID, id.String())
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, d)
		return nil
	})
}

func (s *Server) UpdateFeedback(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireWrite(m); err != nil {
			return err
		}
		var in feedback.UpdateInput
		if err := readJSON(r, &in); err != nil {
			return err
		}
		d, err := s.Feedback.Update(r.Context(), m.OrganizationID, id.String(), m.ID, &in)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, d)
		return nil
	})
}

func (s *Server) AddFeedbackNote(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireWrite(m); err != nil {
			return err
		}
		var in struct{ Note string }
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.Feedback.AddNote(r.Context(), m.OrganizationID, id.String(), m.ID, in.Note); err != nil {
			return err
		}
		platform.WriteJSON(w, 201, map[string]string{"status": "ok"})
		return nil
	})
}

func (s *Server) ListFeedbackActivities(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		acts, err := s.Feedback.Activities(r.Context(), m.OrganizationID, id.String())
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, map[string]any{"activities": acts})
		return nil
	})
}

func (s *Server) DownloadAttachment(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		return s.Attachment.ServeDownload(w, r, m.OrganizationID, id.String())
	})
}

// ---- webhooks (owner/admin only: org-level configuration) ----

func requireAdmin(m *auth.Member) error {
	if m.Role != "owner" && m.Role != "admin" {
		return platform.Errf(403, "forbidden", "Only owners and admins may manage workspace settings")
	}
	return nil
}

func (s *Server) ListWebhookEndpoints(w http.ResponseWriter, r *http.Request) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		eps, err := s.Outbox.ListEndpoints(r.Context(), m.OrganizationID)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, map[string]any{"endpoints": eps})
		return nil
	})
}

func (s *Server) CreateWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		var in struct {
			URL        string   `json:"url"`
			EventTypes []string `json:"event_types"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		ep, err := s.Outbox.CreateEndpoint(r.Context(), m.OrganizationID, in.URL, in.EventTypes)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 201, ep)
		return nil
	})
}

func (s *Server) DeleteWebhookEndpoint(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		if err := s.Outbox.DeleteEndpoint(r.Context(), m.OrganizationID, id.String()); err != nil {
			return err
		}
		platform.WriteJSON(w, 200, map[string]string{"status": "deleted"})
		return nil
	})
}

func (s *Server) UpdateWebhookEndpoint(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		var in struct {
			Status string `json:"status"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.Outbox.SetEndpointStatus(r.Context(), m.OrganizationID, id.String(), in.Status); err != nil {
			return err
		}
		platform.WriteJSON(w, 200, map[string]string{"status": in.Status})
		return nil
	})
}

func (s *Server) ListWebhookDeliveries(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		ds, err := s.Outbox.ListDeliveries(r.Context(), m.OrganizationID, id.String())
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, map[string]any{"deliveries": ds})
		return nil
	})
}

func (s *Server) ReplayWebhookDelivery(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	s.withMember(w, r, func(m *auth.Member) error {
		if err := requireAdmin(m); err != nil {
			return err
		}
		if err := s.Outbox.ReplayDelivery(r.Context(), m.OrganizationID, id.String()); err != nil {
			return err
		}
		platform.WriteJSON(w, 200, map[string]string{"status": "queued"})
		return nil
	})
}

func (s *Server) GetAnalyticsOverview(w http.ResponseWriter, r *http.Request, params GetAnalyticsOverviewParams) {
	s.withMember(w, r, func(m *auth.Member) error {
		projectID := ""
		if params.ProjectId != nil {
			projectID = params.ProjectId.String()
		}
		o, err := s.Feedback.Overview(r.Context(), m.OrganizationID, projectID)
		if err != nil {
			return err
		}
		platform.WriteJSON(w, 200, o)
		return nil
	})
}
