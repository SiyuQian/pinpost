package project

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/smithy-go/middleware"
	"github.com/google/uuid"
)

// Seed's guard is global to a table, so each case needs a table of its own.
func newSeedStore(t *testing.T) *platform.Store {
	t.Helper()
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		os.Setenv("AWS_ENDPOINT_URL", "http://localhost:8000")
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" {
		os.Setenv("AWS_ACCESS_KEY_ID", "pinpost")
		os.Setenv("AWS_SECRET_ACCESS_KEY", "pinpost-secret")
	}
	cfg, err := platform.AWSConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	name := "pinpost-seed-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	store := platform.NewStore(cfg, name)
	if err := platform.EnsureTable(context.Background(), store); err != nil {
		t.Skipf("dynamodb unreachable: %v", err)
	}
	t.Cleanup(func() {
		store.DB.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: &name})
	})
	return store
}

func ownerExists(t *testing.T, store *platform.Store, email string) bool {
	t.Helper()
	members, err := (&auth.Service{Store: store}).MembersByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return len(members) > 0
}

// A Seed that dies partway must leave the table seedable. Getting this wrong
// produces a deployment with no owner and no way to make one.
func TestSeedIsRecoverableAfterPartialFailure(t *testing.T) {
	ctx := context.Background()
	store := newSeedStore(t)

	// bcrypt refuses passwords over 72 bytes, so Provision fails only after
	// the bootstrap marker has already been committed.
	err := Seed(ctx, store, Params{
		OrgName: "Broken", ProjectName: "Default", Environment: "production",
		Email: "owner@example.com", Password: strings.Repeat("x", 80),
	})
	if err == nil {
		t.Fatal("Seed succeeded with an over-long password, expected it to fail")
	}
	if ownerExists(t, store, "owner@example.com") {
		t.Fatal("the failing Seed provisioned an owner anyway; the test no longer covers a partial write")
	}

	if err := Seed(ctx, store, Params{
		OrgName: "Recovered", ProjectName: "Default", Environment: "production",
		Email: "owner@example.com", Password: "correct-horse",
	}); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if !ownerExists(t, store, "owner@example.com") {
		t.Error("the second Seed reported success but provisioned no owner")
	}
}

// SeedDemo must honour the same "only on an unbootstrapped table" gate as
// Seed: its password is published in the README.
func TestSeedDemoDoesNotReseedABootstrappedTable(t *testing.T) {
	ctx := context.Background()
	store := newSeedStore(t)

	if err := Seed(ctx, store, Params{
		OrgName: "Real Customer", ProjectName: "Default", Environment: "production",
		Email: "real@example.com", Password: "correct-horse",
	}); err != nil {
		t.Fatal(err)
	}
	if err := SeedDemo(ctx, store); err != nil {
		t.Fatal(err)
	}
	if ownerExists(t, store, DemoEmail) {
		t.Errorf("SeedDemo planted %s on a table that had already been seeded", DemoEmail)
	}
}

// Rename must update both stored copies of the project: the listing record and
// the write-key copy ingestion authenticates against.
func TestRenameUpdatesBothProjectCopies(t *testing.T) {
	ctx := context.Background()
	store := newSeedStore(t)
	p := Params{OrgName: "Acme", ProjectName: "Default", Environment: "production",
		Email: "owner@example.com", Password: "pw"}
	key, err := Provision(ctx, store, p)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: store}
	orgID := seededOrgID(t, store)
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
	var wk auth.ProjectItem
	k := auth.WriteKeyKey(key)
	if found, err := store.Get(ctx, k.PK, k.SK, &wk); err != nil || !found || wk.Name != "Acme App" {
		t.Fatalf("write-key copy: found=%v name=%q err=%v", found, wk.Name, err)
	}

	if _, err := s.Rename(ctx, orgID, uuid.NewString(), "x"); err == nil {
		t.Fatal("renaming a missing project must 404")
	}
}

// seededOrgID reads the organization Provision created, via the member row.
func seededOrgID(t *testing.T, store *platform.Store) string {
	t.Helper()
	members, err := (&auth.Service{Store: store}).MembersByEmail(context.Background(), "owner@example.com")
	if err != nil || len(members) == 0 {
		t.Fatalf("members %v, err %v", members, err)
	}
	return members[0].OrganizationID
}

// countWrites records how many separate write operations the SDK issues, and
// how many of those were non-transactional PutItem calls.
type writeCounter struct{ ops, puts int }

// storeWithCounter returns a store whose client reports every write operation
// to c. It shares the caller's table.
func storeWithCounter(t *testing.T, store *platform.Store, c *writeCounter) *platform.Store {
	t.Helper()
	cfg, err := platform.AWSConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	counted := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("countWrites",
				func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (
					middleware.InitializeOutput, middleware.Metadata, error) {
					switch in.Parameters.(type) {
					case *dynamodb.PutItemInput:
						c.ops, c.puts = c.ops+1, c.puts+1
					case *dynamodb.TransactWriteItemsInput:
						c.ops++
					}
					return next.HandleInitialize(ctx, in)
				}), middleware.Before)
		})
	})
	return &platform.Store{DB: counted, Table: store.Table}
}

// A project is two items — the org-partition record listings read and the
// WK#<digest> copy ingestion authenticates against — and they must land
// together. More than one write operation means a failure between them
// publishes a project that no write key can reach, which nothing downstream
// can distinguish from a healthy one. Counting the operations pins atomicity
// without needing to inject a failure at the exact wrong moment.
func TestProjectWritesAreOneTransaction(t *testing.T) {
	store := newSeedStore(t)
	ctx := context.Background()

	var provision writeCounter
	if _, err := Provision(ctx, storeWithCounter(t, store, &provision), Params{
		OrgName: "Atomic Org", ProjectName: "Atomic Project", Environment: "development",
		Email: "atomic@pinpost.test", Password: "atomic-pass",
	}); err != nil {
		t.Fatal(err)
	}
	if provision.ops != 1 || provision.puts != 0 {
		t.Errorf("Provision issued %d write operations (%d of them bare PutItem); "+
			"want 1 transaction and no bare Put", provision.ops, provision.puts)
	}

	var create writeCounter
	svc := &Service{Store: storeWithCounter(t, store, &create)}
	orgID := uuid.Must(uuid.NewV7()).String()
	proj, key, err := svc.Create(ctx, orgID, "Created Project", "production")
	if err != nil {
		t.Fatal(err)
	}
	if create.ops != 1 || create.puts != 0 {
		t.Errorf("Create issued %d write operations (%d of them bare PutItem); "+
			"want 1 transaction and no bare Put", create.ops, create.puts)
	}

	var rename writeCounter
	renameSvc := &Service{Store: storeWithCounter(t, store, &rename)}
	if _, err := renameSvc.Rename(ctx, orgID, proj.ID, "Renamed Project"); err != nil {
		t.Fatal(err)
	}
	if rename.ops != 1 || rename.puts != 0 {
		t.Errorf("Rename issued %d write operations (%d of them bare PutItem); "+
			"want 1 transaction and no bare Put", rename.ops, rename.puts)
	}

	// Both copies exist and agree, so the transaction wrote what the two Puts did.
	var byOrg, byKey auth.ProjectItem
	pk := auth.ProjectKey(orgID, proj.ID)
	if found, err := store.Get(ctx, pk.PK, pk.SK, &byOrg); err != nil || !found {
		t.Fatalf("org-partition copy missing: %v", err)
	}
	wk := auth.WriteKeyKey(key)
	if found, err := store.Get(ctx, wk.PK, wk.SK, &byKey); err != nil || !found {
		t.Fatalf("write-key copy missing: %v", err)
	}
	if byOrg.Name != "Renamed Project" || byKey.Name != "Renamed Project" {
		t.Errorf("copies disagree after rename: %q vs %q", byOrg.Name, byKey.Name)
	}
}
