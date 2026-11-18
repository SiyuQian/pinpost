package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SiyuQian/pinpost/internal/project"
	"github.com/google/uuid"
)

// The scan worker replaces the S3 ObjectCreated trigger: a submission claims
// the attachment, and the next ScanDue verifies whatever object has landed.
func TestScanWorkerVerifiesClaimedAttachments(t *testing.T) {
	c := newContractClient(t)
	ctx := context.Background()
	wk := map[string]string{"X-Pinpost-Write-Key": project.DemoWriteKey}
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("a", 40))

	authorize := func(name string) (id, uploadURL string) {
		t.Helper()
		_, res := c.do("POST", "/v1/attachment-uploads", wk,
			[]byte(fmt.Sprintf(`{"original_name":%q,"mime_type":"image/png","size":%d}`, name, len(png))))
		return jsonField(t, res, "attachment_id"), jsonField(t, res, "upload_url")
	}
	scanStatus := func(id string) string {
		t.Helper()
		var s string
		if err := c.db.QueryRow(ctx, `SELECT scan_status FROM attachments WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	uploaded, uploadURL := authorize("landed.png")
	if s := c.putObject(uploadURL, png); s != 200 {
		t.Fatalf("upload: %d", s)
	}
	missing, _ := authorize("never-uploaded.png")
	if s, b := c.do("POST", "/v1/feedback", withIdem(wk, uuid.NewString()),
		[]byte(fmt.Sprintf(`{"type":"bug","message":"scan worker","attachment_ids":[%q,%q]}`, uploaded, missing))); s != 201 {
		t.Fatalf("feedback: %d %s", s, b)
	}

	if n, err := c.srv.Attachment.ScanDue(ctx); err != nil || n != 2 {
		t.Fatalf("ScanDue looked at %d rows (err %v), want 2", n, err)
	}
	if s := scanStatus(uploaded); s != "clean" {
		t.Fatalf("uploaded attachment scan_status = %q, want clean", s)
	}
	if s := scanStatus(missing); s != "pending" {
		t.Fatalf("missing attachment scan_status = %q, want pending until it is given up on", s)
	}
	// The missing one is leased, not retried in a hot loop.
	if n, err := c.srv.Attachment.ScanDue(ctx); err != nil || n != 0 {
		t.Fatalf("immediate rescan looked at %d rows (err %v), want 0", n, err)
	}

	// Past the give-up window the missing upload is recorded as rejected.
	if _, err := c.db.Exec(ctx, `UPDATE attachments
		SET created_at = now() - interval '2 hours', next_scan_at = now() WHERE id = $1`, missing); err != nil {
		t.Fatal(err)
	}
	if _, err := c.srv.Attachment.ScanDue(ctx); err != nil {
		t.Fatal(err)
	}
	if s := scanStatus(missing); s != "rejected" {
		t.Fatalf("never-uploaded attachment scan_status = %q, want rejected", s)
	}
}
