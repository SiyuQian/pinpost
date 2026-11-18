package auth

import (
	"context"
	"testing"

	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newGoogleDB returns a database with one organization recorded as the
// bootstrapped one, which is where Google sign-in places new members.
func newGoogleDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	db := pgtest.New(t)
	orgID := uuid.NewString()
	for _, q := range []string{
		`INSERT INTO organizations (id, name) VALUES ($1, 'Acme')`,
		`INSERT INTO bootstrap (organization_id) VALUES ($1)`,
	} {
		if _, err := db.Exec(context.Background(), q, orgID); err != nil {
			t.Fatal(err)
		}
	}
	return db, orgID
}

const allowedEmail = "alice@example.com"

func googleOK(t *testing.T) GoogleConfig {
	stubGoogle(t, `{"aud":"client-1","email":"`+allowedEmail+`","email_verified":"true","name":"Alice"}`)
	return NewGoogleConfig("client-1", allowedEmail)
}

func TestLoginWithGoogleProvisionsAllowlistedMember(t *testing.T) {
	ctx := context.Background()
	db, orgID := newGoogleDB(t)
	s := &Service{DB: db}
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
	db, orgID := newGoogleDB(t)
	s := &Service{DB: db}

	item := NewMemberItem(orgID, uuid.NewString(), allowedEmail, "Alice", "viewer", "")
	item.Status = "disabled"
	if err := InsertMember(ctx, db, item); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoginWithGoogle(ctx, googleOK(t), "credential"); status(err) != 403 {
		t.Fatalf("got %v (status %d), want 403", err, status(err))
	}
}

// A database that was never seeded has no organization to join. Signing in
// cannot guess one, so it must say so rather than invent one.
func TestLoginWithGoogleBeforeSeeding(t *testing.T) {
	ctx := context.Background()
	s := &Service{DB: pgtest.New(t)}
	if _, _, err := s.LoginWithGoogle(ctx, googleOK(t), "credential"); status(err) != 503 {
		t.Fatalf("got %v (status %d), want 503", err, status(err))
	}
}
