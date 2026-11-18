package auth

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"
)

// The member-creating half of Google sign-in only exists against a table, so
// these run on DynamoDB Local and skip the same way the seed tests do.
func newGoogleStore(t *testing.T) *platform.Store {
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
	name := "pinpost-google-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	store := platform.NewStore(cfg, name)
	if err := platform.EnsureTable(context.Background(), store); err != nil {
		pgtest.Unavailable(t, "dynamodb unreachable: %v", err)
	}
	t.Cleanup(func() {
		store.DB.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: &name})
	})
	return store
}

// putMarker writes the bootstrap marker, with an org id only when one is given
// — a marker without it is what a table seeded by an earlier build looks like.
func putMarker(t *testing.T, store *platform.Store, orgID string) {
	t.Helper()
	item := platform.KeyAV(platform.BootstrapMarker, platform.Meta)
	if orgID != "" {
		item["org_id"] = platform.S(orgID)
	}
	if _, err := store.DB.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: &store.Table, Item: item,
	}); err != nil {
		t.Fatal(err)
	}
}

const allowedEmail = "alice@example.com"

func googleOK(t *testing.T) GoogleConfig {
	stubGoogle(t, `{"aud":"client-1","email":"`+allowedEmail+`","email_verified":"true","name":"Alice"}`)
	return NewGoogleConfig("client-1", allowedEmail)
}

func TestLoginWithGoogleProvisionsAllowlistedMember(t *testing.T) {
	ctx := context.Background()
	store := newGoogleStore(t)
	orgID := uuid.NewString()
	putMarker(t, store, orgID)
	s := &Service{Store: store}
	cfg := googleOK(t)

	_, first, err := s.LoginWithGoogle(ctx, cfg, "credential")
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	if first.OrganizationID != orgID || first.Role != "admin" || first.Email != allowedEmail {
		t.Fatalf("member %+v, want org %s role admin", first, orgID)
	}

	// A second sign-in must reuse the row rather than pile up duplicates, and
	// refresh name and picture from Google's profile.
	stubGoogle(t, `{"aud":"client-1","email":"`+allowedEmail+`","email_verified":"true","name":"Alice Q","picture":"https://lh3.example/p.jpg"}`)
	_, second, err := s.LoginWithGoogle(ctx, cfg, "credential")
	if err != nil {
		t.Fatalf("second sign-in: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second sign-in created a new member %s, want %s", second.ID, first.ID)
	}
	if second.Name != "Alice Q" || second.Picture != "https://lh3.example/p.jpg" {
		t.Fatalf("profile not refreshed: %+v", second)
	}
}

// Deactivating the row is the only revocation lever there is; Google sign-in
// must honour it instead of minting a fresh admin over the top.
func TestLoginWithGoogleRejectsDeactivatedMember(t *testing.T) {
	ctx := context.Background()
	store := newGoogleStore(t)
	orgID := uuid.NewString()
	putMarker(t, store, orgID)
	s := &Service{Store: store}

	item := NewMemberItem(orgID, uuid.NewString(), allowedEmail, "Alice", "viewer", "")
	item.Status = "disabled"
	if err := store.Put(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoginWithGoogle(ctx, googleOK(t), "credential"); status(err) != 403 {
		t.Fatalf("got %v (status %d), want 403", err, status(err))
	}
}

// A marker with no org id is a table seeded before the id was recorded there.
// Signing in cannot guess an organization, so it must say so rather than
// invent one — the backfill in project.Seed is what repairs it.
func TestLoginWithGoogleWithoutMarkerOrg(t *testing.T) {
	ctx := context.Background()
	store := newGoogleStore(t)
	putMarker(t, store, "")
	s := &Service{Store: store}
	if _, _, err := s.LoginWithGoogle(ctx, googleOK(t), "credential"); status(err) != 503 {
		t.Fatalf("got %v (status %d), want 503", err, status(err))
	}
}
