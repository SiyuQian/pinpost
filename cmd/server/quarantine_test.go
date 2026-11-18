package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/SiyuQian/pinpost/internal/project"
	"github.com/google/uuid"
)

// TestMaliciousAttachmentQuarantine uploads a valid PNG carrying the EICAR
// test string, expects the scan handler to flag it, and expects it to stay
// linkable to feedback (metadata only) but blocked from member download.
func TestMaliciousAttachmentQuarantine(t *testing.T) {
	c := newContractClient(t)
	wk := map[string]string{"X-Pinpost-Write-Key": project.DemoWriteKey}
	eicar := `X5O!P%@AP[4\PZX54(P^)7CC)7}$` + `EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`
	evil := []byte("\x89PNG\r\n\x1a\n" + eicar)

	attID := c.upload(wk, "evil.png", "image/png", evil)

	var item struct{ ScanStatus string }
	err := c.db.QueryRow(context.Background(), `SELECT scan_status FROM attachments WHERE id = $1`, attID).Scan(&item.ScanStatus)
	if found := err == nil; err != nil || !found {
		t.Fatalf("attachment item: found=%v err=%v", found, err)
	}
	if item.ScanStatus != "flagged" {
		t.Fatalf("scan_status = %q, want flagged", item.ScanStatus)
	}

	// Still attachable — dashboard shows it with its flagged status.
	if s, b := c.do("POST", "/v1/feedback", withIdem(wk, uuid.NewString()),
		[]byte(fmt.Sprintf(`{"type":"bug","message":"quarantine test","attachment_ids":[%q]}`, attID))); s != 201 {
		t.Fatalf("feedback with flagged attachment: %d %s", s, b)
	}

	c.do("POST", "/v1/auth/login", nil,
		[]byte(fmt.Sprintf(`{"Email":%q,"Password":%q}`, project.DemoEmail, project.DemoPassword)))
	if s, _ := c.do("GET", "/v1/attachments/"+attID+"/download", nil, nil); s != 409 {
		t.Fatalf("flagged download: want 409, got %d", s)
	}
}

// TestMislabeledAttachmentRejected covers the behaviour change from the
// migration: a file whose real type does not match the declared one used to be
// refused at upload time; now it lands in the bucket and the scan handler
// records the rejection instead.
func TestMislabeledAttachmentRejected(t *testing.T) {
	c := newContractClient(t)
	wk := map[string]string{"X-Pinpost-Write-Key": project.DemoWriteKey}

	attID := c.upload(wk, "notreally.png", "image/png", []byte("plain text pretending to be a png"))

	var item struct{ ScanStatus string }
	if err := c.db.QueryRow(context.Background(), `SELECT scan_status FROM attachments WHERE id = $1`, attID).Scan(&item.ScanStatus); err != nil {
		t.Fatal(err)
	}
	if item.ScanStatus != "rejected" {
		t.Fatalf("scan_status = %q, want rejected", item.ScanStatus)
	}
	c.do("POST", "/v1/auth/login", nil,
		[]byte(fmt.Sprintf(`{"Email":%q,"Password":%q}`, project.DemoEmail, project.DemoPassword)))
	if s, _ := c.do("GET", "/v1/attachments/"+attID+"/download", nil, nil); s != 409 {
		t.Fatalf("rejected download: want 409, got %d", s)
	}
}
