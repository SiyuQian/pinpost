package project

import (
	"context"
	"sync"
	"testing"

	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ownerExists(t *testing.T, db *pgxpool.Pool, email string) bool {
	t.Helper()
	members, err := (&auth.Service{DB: db}).MembersByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return len(members) > 0
}

func bootstrapped(t *testing.T, db *pgxpool.Pool) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM bootstrap)`).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// A Seed that dies partway must leave the database seedable. Getting this
// wrong produces a deployment with no owner and no way to make one.
func TestSeedIsRecoverableAfterPartialFailure(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)

	// A write key already in use fails the project insert — after the
	// organization row has been written in the same transaction.
	if _, err := Provision(ctx, db, Params{OrgName: "Other", ProjectName: "Other",
		Environment: "development", Email: "other@example.com", Password: "pw", WriteKey: "wk_taken"}); err != nil {
		t.Fatal(err)
	}
	err := Seed(ctx, db, Params{
		OrgName: "Broken", ProjectName: "Default", Environment: "production",
		Email: "owner@example.com", Password: "correct-horse", WriteKey: "wk_taken",
	})
	if err == nil {
		t.Fatal("Seed succeeded with a duplicate write key, expected it to fail")
	}
	if ownerExists(t, db, "owner@example.com") || bootstrapped(t, db) {
		t.Fatal("the failing Seed left part of its tenant behind")
	}

	if err := Seed(ctx, db, Params{
		OrgName: "Recovered", ProjectName: "Default", Environment: "production",
		Email: "owner@example.com", Password: "correct-horse",
	}); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if !ownerExists(t, db, "owner@example.com") || !bootstrapped(t, db) {
		t.Error("the second Seed reported success but provisioned no owner")
	}
}

// SeedDemo must honour the same "only on an unbootstrapped database" gate as
// Seed: its password is published in the README.
func TestSeedDemoDoesNotReseedABootstrappedDatabase(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)

	if err := Seed(ctx, db, Params{
		OrgName: "Real Customer", ProjectName: "Default", Environment: "production",
		Email: "real@example.com", Password: "correct-horse",
	}); err != nil {
		t.Fatal(err)
	}
	if err := SeedDemo(ctx, db); err != nil {
		t.Fatal(err)
	}
	if ownerExists(t, db, DemoEmail) {
		t.Errorf("SeedDemo planted %s on a database that had already been seeded", DemoEmail)
	}
}

// Replicas booting together all run Seed; exactly one tenant may result.
func TestConcurrentSeedsProvisionOneTenant(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Seed(ctx, db, Params{OrgName: "Acme", ProjectName: "Default",
				Environment: "production", Email: "owner@example.com", Password: "pw"})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var orgs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM organizations`).Scan(&orgs); err != nil || orgs != 1 {
		t.Fatalf("organizations = %d (err %v), want 1", orgs, err)
	}
}

func TestRenameAndRotateWriteKey(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)
	key, err := Provision(ctx, db, Params{OrgName: "Acme", ProjectName: "Default",
		Environment: "production", Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db}
	orgID := seededOrgID(t, db)
	projects, err := s.ListProjects(ctx, orgID)
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects %v, err %v", projects, err)
	}

	renamed, err := s.Rename(ctx, orgID, projects[0].ID, "Acme App")
	if err != nil || renamed.Name != "Acme App" {
		t.Fatalf("renamed %+v, err %v", renamed, err)
	}
	projects, _ = s.ListProjects(ctx, orgID)
	if projects[0].Name != "Acme App" {
		t.Fatalf("listing still says %q", projects[0].Name)
	}
	if _, err := s.Rename(ctx, orgID, uuid.NewString(), "x"); err == nil {
		t.Fatal("renaming a missing project must 404")
	}
	// Another organization's id must not reach this project.
	if _, err := s.Rename(ctx, uuid.NewString(), projects[0].ID, "x"); err == nil {
		t.Fatal("renaming through another organization must 404")
	}

	rotated, err := s.RotateWriteKey(ctx, orgID, projects[0].ID)
	if err != nil || rotated.WriteKey == key {
		t.Fatalf("rotated %+v, err %v", rotated, err)
	}
	a := &auth.Service{DB: db}
	if _, err := a.ProjectByWriteKey(ctx, key); err == nil {
		t.Fatal("the old write key still authenticates after rotation")
	}
	if p, err := a.ProjectByWriteKey(ctx, rotated.WriteKey); err != nil || p.ProjectID != rotated.ID {
		t.Fatalf("new key resolves to %+v, err %v", p, err)
	}
}

// seededOrgID reads the organization Provision created, via the member row.
func seededOrgID(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	members, err := (&auth.Service{DB: db}).MembersByEmail(context.Background(), "owner@example.com")
	if err != nil || len(members) == 0 {
		t.Fatalf("members %v, err %v", members, err)
	}
	return members[0].OrganizationID
}
