package project

import (
	"context"
	"errors"

	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service exposes org-scoped project and member reads and writes. Every query
// filters on the organization from the caller's session.
type Service struct{ DB *pgxpool.Pool }

// Org is the organization root, read for the workspace header.
type Org struct {
	ID   string `json:"ID"`
	Name string `json:"Name"`
}

func (s *Service) Organization(ctx context.Context, orgID string) (Org, error) {
	var name string
	err := s.DB.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, orgID).Scan(&name)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Org{}, err
	}
	// A member's session pins the org id, so a missing or nameless root record
	// is a half-provisioned tenant, not a bad request — booting the dashboard
	// matters more than the name. It is named by id rather than by a generic
	// literal, so the broken tenant is identifiable from the header instead of
	// looking exactly like a healthy one.
	if name == "" {
		return Org{ID: orgID, Name: "工作区 " + fallbackSuffix(orgID)}, nil
	}
	return Org{ID: orgID, Name: name}, nil
}

func fallbackSuffix(orgID string) string {
	if len(orgID) > 8 {
		return orgID[:8]
	}
	return orgID
}

// Project is what the console lists. WriteKey is the public ingestion key.
type Project struct {
	ID          string `db:"id"`
	Name        string `db:"name"`
	Environment string `db:"environment"`
	WriteKey    string `db:"write_key" json:",omitempty"`
}

const projectCols = `id, name, environment, write_key`

func (s *Service) ListProjects(ctx context.Context, orgID string) ([]Project, error) {
	rows, _ := s.DB.Query(ctx, `SELECT `+projectCols+` FROM projects
		WHERE organization_id = $1 ORDER BY id`, orgID)
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[Project])
	if out == nil {
		out = []Project{}
	}
	return out, err
}

// Create provisions a new project inside an existing organization and returns
// it together with the write key. Environment is empty-optional and defaults
// to "development".
func (s *Service) Create(ctx context.Context, orgID, name, environment string) (Project, string, error) {
	if environment == "" {
		environment = "development"
	}
	p := Project{ID: uuid.Must(uuid.NewV7()).String(), Name: name, Environment: environment,
		WriteKey: auth.NewWriteKey()}
	if _, err := s.DB.Exec(ctx, `INSERT INTO projects (id, organization_id, name, environment, write_key)
		VALUES ($1, $2, $3, $4, $5)`, p.ID, orgID, p.Name, p.Environment, p.WriteKey); err != nil {
		return Project{}, "", err
	}
	return p, p.WriteKey, nil
}

// RenameOrg updates the organization root record's name. A half-provisioned
// tenant has no root record to rename, and writing one here would paper over
// the broken seed Organization() flags.
func (s *Service) RenameOrg(ctx context.Context, orgID, name string) (Org, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE organizations SET name = $2 WHERE id = $1`, orgID, name)
	if err != nil {
		return Org{}, err
	}
	if tag.RowsAffected() == 0 {
		return Org{}, platform.Errf(404, "not_found", "Organization not found")
	}
	return Org{ID: orgID, Name: name}, nil
}

// updateProject runs one UPDATE on a project of this organization and returns
// the row as it now stands.
func (s *Service) updateProject(ctx context.Context, set string, orgID, projectID string, arg any) (Project, error) {
	rows, _ := s.DB.Query(ctx, `UPDATE projects SET `+set+`
		WHERE organization_id = $1 AND id = $2 RETURNING `+projectCols, orgID, projectID, arg)
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Project])
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, platform.Errf(404, "not_found", "Project not found")
	}
	return p, err
}

func (s *Service) Rename(ctx context.Context, orgID, projectID, name string) (Project, error) {
	return s.updateProject(ctx, `name = $3`, orgID, projectID, name)
}

// RotateWriteKey replaces a project's write key. It is one row, so the old key
// stops authenticating at the instant the new one starts: there is never a
// moment with zero or two valid keys. Deployed SDKs holding the old key get
// 401 from then on — that is the point of rotating, and the console says so
// before it asks.
//
// ponytail: no grace window. Add a second, expiring key column if a customer
// needs a zero-downtime roll.
func (s *Service) RotateWriteKey(ctx context.Context, orgID, projectID string) (Project, error) {
	return s.updateProject(ctx, `write_key = $3`, orgID, projectID, auth.NewWriteKey())
}

type Member struct {
	ID      string `db:"id"`
	Email   string `db:"email"`
	Name    string `db:"name"`
	Role    string `db:"role"`
	Picture string `db:"picture" json:",omitempty"`
}

func (s *Service) ListMembers(ctx context.Context, orgID string) ([]Member, error) {
	rows, _ := s.DB.Query(ctx, `SELECT id, email, name, role, picture FROM members
		WHERE organization_id = $1 AND status = 'active' ORDER BY id`, orgID)
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[Member])
	if out == nil {
		out = []Member{}
	}
	return out, err
}

// IsMember reports whether memberID is an active member of the organization.
func (s *Service) IsMember(ctx context.Context, orgID, memberID string) (bool, error) {
	if uuid.Validate(memberID) != nil {
		return false, nil
	}
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM members
		WHERE organization_id = $1 AND id = $2 AND status = 'active')`, orgID, memberID).Scan(&ok)
	return ok, err
}
