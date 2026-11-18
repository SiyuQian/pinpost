// Package project owns organizations, projects and members.
package project

import (
	"context"
	"log/slog"
	"time"

	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

const (
	// DemoWriteKey is a fixed key so the local demo page works out of the box.
	DemoWriteKey = "wk_demo_5f8a2c91d4e7b3a6f0c1"
	DemoEmail    = "admin@example.com"
	DemoPassword = "pinpost-demo"
	demoOrgName  = "Demo Org"
	demoProjName = "Demo Project"
	demoSettings = `{"types":[{"key":"bug","label":"Bug","enabled":true},{"key":"suggestion","label":"Suggestion","enabled":true},{"key":"other","label":"Other","enabled":true}],"allow_rating":true,"allow_attachments":true,"max_attachments":3}`

	// Fixed demo identifiers keep re-seeding an existing local table idempotent.
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
// known password and the fixed write key can never land on a table that has
// already been seeded with real data.
func SeedDemo(ctx context.Context, store *platform.Store) error {
	return Seed(ctx, store, Params{
		OrgID: demoOrgID, ProjectID: demoProjectID, MemberID: demoMemberID,
		OrgName: demoOrgName, ProjectName: demoProjName, Environment: "development",
		Email: DemoEmail, Password: DemoPassword, WriteKey: DemoWriteKey, Settings: demoSettings,
	})
}

// Seed creates the first org/project/owner so a fresh deployment has somewhere
// to sign in. Idempotent, and deliberately keyed on "the system has been
// bootstrapped" rather than a name: once it holds real data it must never
// re-seed. The check is a conditional write on one marker item — no Scan.
func Seed(ctx context.Context, store *platform.Store, p Params) error {
	// The organization id is chosen here rather than inside Provision so the
	// marker can carry it: Google sign-in reads it to place a new member.
	if p.OrgID == "" {
		p.OrgID = uuid.Must(uuid.NewV7()).String()
	}
	marker := platform.KeyAV(platform.BootstrapMarker, platform.Meta)
	marker["org_id"] = platform.S(p.OrgID)
	_, err := store.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &store.Table,
		Item:                marker,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if platform.ConditionFailed(err) {
		backfillMarkerOrg(ctx, store) // already seeded
		return nil
	}
	if err != nil {
		return err
	}
	if p.Settings == "" {
		p.Settings = demoSettings
	}
	key, err := Provision(ctx, store, p)
	if err != nil {
		// The marker is the mutual exclusion, not the record of success. Leave
		// it behind after a partial write and every later boot reports
		// "already seeded" — a deployment with no owner and no way to make one.
		if _, delErr := store.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: &store.Table,
			Key:       platform.KeyAV(platform.BootstrapMarker, platform.Meta),
		}); delErr != nil {
			slog.Error("seed failed and the bootstrap marker could not be rolled back; "+
				"delete it by hand or this deployment will never seed",
				"marker", platform.BootstrapMarker, "error", delErr)
		}
		return err
	}
	// The write key is public by design; log it once so the SDK can be configured.
	slog.Info("seeded initial tenant", "org", p.OrgName, "email", p.Email, "write_key", key)
	return nil
}

// backfillMarkerOrg stamps the organization onto a marker written before the id
// was recorded there. Tables seeded by an earlier build have a marker with
// pk/sk only, and Google sign-in reads org_id to place a new member — without
// this, every first sign-in on such a deployment gets 503 forever.
//
// Failures are logged, not returned: a boot that cannot complete the backfill
// still serves every existing member, and the 503 names what is missing.
//
// ponytail: one Scan, run only while the attribute is absent, on a deployment
// with exactly one organization. Multiple orgs make this an explicit choice
// rather than a lookup, and the function refuses instead of guessing.
func backfillMarkerOrg(ctx context.Context, store *platform.Store) {
	var marker struct {
		OrganizationID string `dynamodbav:"org_id"`
	}
	found, err := store.Get(ctx, platform.BootstrapMarker, platform.Meta, &marker)
	if err != nil || !found || marker.OrganizationID != "" {
		return
	}
	out, err := store.DB.Scan(ctx, &dynamodb.ScanInput{
		TableName:                 &store.Table,
		FilterExpression:          aws.String("begins_with(pk, :org) AND sk = :meta"),
		ProjectionExpression:      aws.String("org_id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":org": platform.S("ORG#"), ":meta": platform.S(platform.Meta)},
	})
	if err != nil || len(out.Items) != 1 {
		slog.Error("the bootstrap marker carries no org_id and the organization could not be resolved; "+
			"set it by hand or Google sign-in will not be able to create members",
			"marker", platform.BootstrapMarker, "organizations", len(out.Items), "error", err)
		return
	}
	var org OrgItem
	if err := attributevalue.UnmarshalMap(out.Items[0], &org); err != nil || org.OrganizationID == "" {
		slog.Error("could not read the organization id while backfilling the bootstrap marker", "error", err)
		return
	}
	if _, err := store.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &store.Table,
		Key:                       platform.KeyAV(platform.BootstrapMarker, platform.Meta),
		UpdateExpression:          aws.String("SET org_id = :id"),
		ConditionExpression:       aws.String("attribute_not_exists(org_id)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":id": platform.S(org.OrganizationID)},
	}); err != nil && !platform.ConditionFailed(err) {
		slog.Error("could not backfill the bootstrap marker", "error", err)
		return
	}
	slog.Info("backfilled the bootstrap marker with its organization", "org", org.OrganizationID)
}

// Provision writes one complete tenant (organization, project, owner) and
// returns the project's write key. Callers that need "only if empty" semantics
// go through Seed.
func Provision(ctx context.Context, store *platform.Store, p Params) (string, error) {
	newID := func(v string) string {
		if v != "" {
			return v
		}
		return uuid.Must(uuid.NewV7()).String()
	}
	orgID, projID, memberID := newID(p.OrgID), newID(p.ProjectID), newID(p.MemberID)
	if p.WriteKey == "" {
		p.WriteKey = auth.NewWriteKey()
	}
	if p.Settings == "" {
		p.Settings = "{}"
	}
	pwHash, err := auth.HashPassword(p.Password)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()

	org := OrgItem{
		Key:            platform.Key{PK: "ORG#" + orgID, SK: platform.Meta},
		OrganizationID: orgID, Name: p.OrgName, CreatedAt: now,
	}
	proj := auth.ProjectItem{
		Key:            auth.ProjectKey(orgID, projID),
		OrganizationID: orgID, ProjectID: projID, Name: p.ProjectName,
		Environment: p.Environment, WriteKeyHash: auth.HashWriteKey(p.WriteKey),
		WriteKey: p.WriteKey, Settings: p.Settings, CreatedAt: now,
	}
	// Second copy keyed by the write-key digest: ingestion authenticates with
	// one GetItem instead of a lookup plus a join.
	wkCopy := proj
	wkCopy.Key = auth.WriteKeyKey(p.WriteKey)
	// A tenant is only usable whole: an organization without its owner, or a
	// project without its write-key copy, is a half-provisioned deployment
	// Seed would then mark as bootstrapped. One transaction or nothing.
	if err := store.PutAll(ctx, &org, &proj, &wkCopy,
		auth.NewMemberItem(orgID, memberID, p.Email, "Owner", "owner", pwHash)); err != nil {
		return "", err
	}
	return p.WriteKey, nil
}

// AddMember provisions an extra member in an existing organization.
func AddMember(ctx context.Context, store *platform.Store, orgID, email, name, role, password string) (string, error) {
	pwHash, err := auth.HashPassword(password)
	if err != nil {
		return "", err
	}
	id := uuid.Must(uuid.NewV7()).String()
	return id, store.Put(ctx, auth.NewMemberItem(orgID, id, email, name, role, pwHash))
}
