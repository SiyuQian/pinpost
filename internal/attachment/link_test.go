package attachment

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamotypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

// LinkTransactItem's condition expression is the only thing standing between a
// submission and another project's attachment. Nothing else asserts it, so an
// edit that drops `project_id = :proj` would be a silent cross-tenant leak.
func newLinkStore(t *testing.T) *platform.Store {
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
	store := platform.NewStore(cfg, "pinpost-test")
	if err := platform.EnsureTable(context.Background(), store); err != nil {
		t.Skipf("dynamodb unreachable: %v", err)
	}
	return store
}

func putPending(t *testing.T, store *platform.Store, attID, projectID string) {
	t.Helper()
	if err := store.Put(context.Background(), &Item{
		Key: itemKey(attID), AttachmentID: attID,
		OrganizationID: "org-1", ProjectID: projectID,
		ObjectKey: projectID + "/" + attID, OriginalName: "shot.png",
		MimeType: "image/png", Size: 12, UploadStatus: "pending",
		ScanStatus: "pending", CreatedAt: time.Now().UTC(),
		ExpiresAt: platform.TTL(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
}

func link(store *platform.Store, attID, projectID, feedbackID string) error {
	_, err := store.DB.TransactWriteItems(context.Background(), &dynamodb.TransactWriteItemsInput{
		TransactItems: []dynamotypes.TransactWriteItem{
			LinkTransactItem(store, attID, projectID, feedbackID)},
	})
	return err
}

func cancelled(t *testing.T, err error) {
	t.Helper()
	var c *dynamotypes.TransactionCanceledException
	if !errors.As(err, &c) {
		t.Fatalf("error = %v, want the transaction to be cancelled", err)
	}
	for _, r := range c.CancellationReasons {
		if r.Code != nil && *r.Code == "ConditionalCheckFailed" {
			return
		}
	}
	t.Fatalf("cancelled, but not by the condition: %+v", c.CancellationReasons)
}

func TestLinkRejectsForeignProject(t *testing.T) {
	store := newLinkStore(t)
	attID := uuid.NewString()
	putPending(t, store, attID, "project-A")

	cancelled(t, link(store, attID, "project-B", uuid.NewString()))
}

func TestLinkRejectsDoubleClaim(t *testing.T) {
	store := newLinkStore(t)
	attID := uuid.NewString()
	putPending(t, store, attID, "project-A")

	if err := link(store, attID, "project-A", uuid.NewString()); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	cancelled(t, link(store, attID, "project-A", uuid.NewString()))
}

func TestLinkRejectsUnknownAttachment(t *testing.T) {
	store := newLinkStore(t)
	cancelled(t, link(store, uuid.NewString(), "project-A", uuid.NewString()))
}
