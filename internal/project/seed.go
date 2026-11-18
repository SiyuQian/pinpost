// Package project owns organizations, projects and members.
package project

import (
	"context"
	"errors"
	"log/slog"

	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// DemoWriteKey is a fixed key so the local demo page works out of the box.
	DemoWriteKey = "wk_demo_5f8a2c91d4e7b3a6f0c1"
	DemoEmail    = "admin@example.com"
	DemoPassword = "pinpost-demo"
	demoOrgName  = "Demo Org"
	demoProjName = "Demo Project"
	demoSettings = `{"types":[{"key":"bug","label":"Bug","enabled":true},{"key":"suggestion","label":"Suggestion","enabled":true},{"key":"other","label":"Other","enabled":true}],"allow_rating":true,"allow_attachments":true,"max_attachments":3}`

	// Fixed demo identifiers, so the demo tenant looks the same on every
	// fresh database.
	demoOrgID     = "01900000-0000-7000-8000-000000000001"
	demoProjectID = "01900000-0000-7000-8000-000000000002"
	demoMemberID  = "01900000-0000-7000-8000-000000000003"
)

// Params describes one tenant to provision. Empty IDs are generated.
type Params struct {
	OrgID, ProjectID, MemberID          string
	OrgName, ProjectName, Environment   string
	Email, Password, WriteKey, Settings string
}

// SeedDemo creates the demo tenant with its published credentials. Local
// development and tests only — main gates it behind PINPOST_SEED_DEMO because
// the password is in the README. It goes through Seed, not Provision, so the
// known password and the fixed write key can never land on a database that
// has already been seeded with real data.
func SeedDemo(ctx context.Context, db *pgxpool.Pool) error {
	return Seed(ctx, db, Params{
		OrgID: demoOrgID, ProjectID: demoProjectID, MemberID: demoMemberID,
		OrgName: demoOrgName, ProjectName: demoProjName, Environment: "development",
		Email: DemoEmail, Password: DemoPassword, WriteKey: DemoWriteKey, Settings: demoSettings,
	})
}

var errAlreadySeeded = errors.New("already seeded")

// Seed creates the first org/project/owner so a fresh deployment has somewhere
// to sign in. Idempotent, and deliberately keyed on "the system has been
// bootstrapped" rather than a name: once it holds real data it must never
// re-seed. The tenant and the bootstrap row commit together, so a Seed that
// fails partway leaves nothing behind and the next boot simply tries again.
func Seed(ctx context.Context, db *pgxpool.Pool, p Params) error {
	var seeded bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bootstrap)`).Scan(&seeded); err != nil || seeded {
		return err
	}
	if p.Settings == "" {
		p.Settings = demoSettings
	}
	p, pwHash, err := prepare(p)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := provision(ctx, tx, p, pwHash); err != nil {
			return err
		}
		// Two replicas booting at once both get here; the second waits on the
		// primary key, then inserts nothing and rolls its tenant back.
		tag, err := tx.Exec(ctx, `INSERT INTO bootstrap (organization_id) VALUES ($1)
			ON CONFLICT DO NOTHING`, p.OrgID)
		if err == nil && tag.RowsAffected() == 0 {
			return errAlreadySeeded
		}
		return err
	})
	if errors.Is(err, errAlreadySeeded) {
		return nil
	}
	if err != nil {
		return err
	}
	// The write key is public by design; log it once so the SDK can be configured.
	slog.Info("seeded initial tenant", "org", p.OrgName, "email", p.Email, "write_key", p.WriteKey)
	return nil
}

// Provision writes one complete tenant (organization, project, owner) in one
// transaction and returns the project's write key. Callers that need "only if
// empty" semantics go through Seed.
func Provision(ctx context.Context, db *pgxpool.Pool, p Params) (string, error) {
	p, pwHash, err := prepare(p)
	if err != nil {
		return "", err
	}
	return p.WriteKey, pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		return provision(ctx, tx, p, pwHash)
	})
}

// prepare fills in generated values and hashes the password, outside any
// transaction: bcrypt is slow and needs no row locks.
func prepare(p Params) (Params, string, error) {
	newID := func(v string) string {
		if v != "" {
			return v
		}
		return uuid.Must(uuid.NewV7()).String()
	}
	p.OrgID, p.ProjectID, p.MemberID = newID(p.OrgID), newID(p.ProjectID), newID(p.MemberID)
	if p.WriteKey == "" {
		p.WriteKey = auth.NewWriteKey()
	}
	if p.Settings == "" {
		p.Settings = "{}"
	}
	pwHash, err := auth.HashPassword(p.Password)
	return p, pwHash, err
}

func provision(ctx context.Context, tx pgx.Tx, p Params, pwHash string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1, $2)`,
		p.OrgID, p.OrgName); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO projects (id, organization_id, name, environment, write_key, settings)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		p.ProjectID, p.OrgID, p.ProjectName, p.Environment, p.WriteKey, p.Settings); err != nil {
		return err
	}
	return auth.InsertMember(ctx, tx, auth.NewMemberItem(p.OrgID, p.MemberID, p.Email, "Owner", "owner", pwHash))
}

// AddMember provisions an extra member in an existing organization.
func AddMember(ctx context.Context, db *pgxpool.Pool, orgID, email, name, role, password string) (string, error) {
	pwHash, err := auth.HashPassword(password)
	if err != nil {
		return "", err
	}
	id := uuid.Must(uuid.NewV7()).String()
	return id, auth.InsertMember(ctx, db, auth.NewMemberItem(orgID, id, email, name, role, pwHash))
}
