package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/google/uuid"
)

// GoogleConfig gates Google Sign-In. An empty ClientID disables the endpoint
// outright; an empty Allowed list means nobody may sign in, because an
// allow-everyone default on a dashboard is the wrong way to fail.
type GoogleConfig struct {
	ClientID string
	Allowed  []string
}

// NewGoogleConfig parses the comma-separated allowlist into normalized emails.
func NewGoogleConfig(clientID, allowed string) GoogleConfig {
	c := GoogleConfig{ClientID: strings.TrimSpace(clientID)}
	for _, e := range strings.Split(allowed, ",") {
		if e = NormalizeEmail(e); e != "" {
			c.Allowed = append(c.Allowed, e)
		}
	}
	return c
}

func (c GoogleConfig) Enabled() bool { return c.ClientID != "" && len(c.Allowed) > 0 }

func (c GoogleConfig) allows(email string) bool {
	for _, e := range c.Allowed {
		if e == email {
			return true
		}
	}
	return false
}

// googleClient is deliberately short-lived: a login that cannot reach Google
// within a couple of seconds should fail, not hold the request open.
var googleClient = &http.Client{Timeout: 5 * time.Second}

// googleTokenInfo is Google's tokeninfo response. Numeric claims come back as
// strings, which is why exp is not parsed here — the endpoint rejects expired
// tokens itself, so reaching a 200 already means the token is live.
type googleTokenInfo struct {
	Aud           string `json:"aud"`
	Email         string `json:"email"`
	EmailVerified string `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// verifyGoogleIDToken checks the credential with Google rather than locally.
//
// ponytail: one HTTPS round trip per sign-in. Two people sign in here, so the
// latency is free; switch to local JWKS verification (google.golang.org/api/idtoken)
// if login volume ever makes the hop matter.
func verifyGoogleIDToken(ctx context.Context, credential string) (*googleTokenInfo, error) {
	invalid := platform.Errf(401, "invalid_credentials", "Google sign-in could not be verified")
	if credential == "" {
		return nil, invalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://oauth2.googleapis.com/tokeninfo?id_token="+url.QueryEscape(credential), nil)
	if err != nil {
		return nil, err
	}
	res, err := googleClient.Do(req)
	if err != nil {
		return nil, platform.Errf(502, "google_unreachable", "Could not reach Google to verify the sign-in")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, invalid
	}
	var info googleTokenInfo
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		return nil, invalid
	}
	return &info, nil
}

// LoginWithGoogle exchanges a Google ID token for a dashboard session. The
// allowlist is the access control: a member row is created on first sign-in so
// nobody has to hand-write DynamoDB items to onboard an allowed address.
//
// The allowlist is consulted on every sign-in, so removing an address stops the
// next one. It does not reach sessions already issued — those run out with the
// 12-hour TTL. Cutting access immediately means deactivating the member row as
// well, which both login paths honour; the README says so next to the variable.
func (s *Service) LoginWithGoogle(ctx context.Context, cfg GoogleConfig, credential string) (string, *Member, error) {
	if !cfg.Enabled() {
		return "", nil, platform.Errf(404, "google_disabled", "Google sign-in is not configured")
	}
	info, err := verifyGoogleIDToken(ctx, credential)
	if err != nil {
		return "", nil, err
	}
	// The audience check is what stops a token minted for some other site from
	// being replayed here; without it any valid Google token would pass.
	if !ConstantTimeEqual(info.Aud, cfg.ClientID) || info.EmailVerified != "true" {
		return "", nil, platform.Errf(401, "invalid_credentials", "Google sign-in could not be verified")
	}
	email := NormalizeEmail(info.Email)
	if !cfg.allows(email) {
		return "", nil, platform.Errf(403, "not_allowed", "This Google account is not allowed to sign in")
	}

	item, err := s.memberForGoogle(ctx, email, info.Name, info.Picture)
	if err != nil {
		return "", nil, err
	}
	token, err := s.newSession(ctx, item)
	if err != nil {
		return "", nil, err
	}
	return token, memberOf(item), nil
}

// memberForGoogle finds the allowed address's member row, creating one in the
// bootstrapped organization the first time that address signs in. Google's
// profile is the source of truth for name and picture on this path, so an
// existing row — including the seeded "Owner" placeholder — is refreshed when
// they change.
func (s *Service) memberForGoogle(ctx context.Context, email, name, picture string) (*MemberItem, error) {
	items, err := s.MembersByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Status == "active" {
			item := &items[i]
			if (name != "" && name != item.Name) || picture != item.Picture {
				if name != "" {
					item.Name = name
				}
				item.Picture = picture
				if err := s.Store.Put(ctx, item); err != nil {
					return nil, err
				}
			}
			return item, nil
		}
	}
	// Rows exist but none is active: the address was deactivated, which is this
	// codebase's only revocation lever and one the password path honours.
	// Minting a replacement here would undo it — and hand back "admin" whatever
	// role the disabled row had.
	if len(items) > 0 {
		return nil, platform.Errf(403, "not_allowed", "This account has been deactivated")
	}
	orgID, err := s.bootstrapOrgID(ctx)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = email
	}
	// No password hash: this row can only ever be reached through Google,
	// because bcrypt rejects an empty hash for every candidate password.
	item := NewMemberItem(orgID, uuid.Must(uuid.NewV7()).String(), email, name, "admin", "")
	item.Picture = picture
	if err := s.Store.Put(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

// bootstrapOrgID reads the organization stamped on the seed marker. There is
// exactly one organization in this deployment; when that stops being true this
// has to become an explicit choice instead of a lookup.
func (s *Service) bootstrapOrgID(ctx context.Context) (string, error) {
	var marker struct {
		OrganizationID string `dynamodbav:"org_id"`
	}
	found, err := s.Store.Get(ctx, platform.BootstrapMarker, platform.Meta, &marker)
	if err != nil {
		return "", err
	}
	if !found || marker.OrganizationID == "" {
		return "", platform.Errf(503, "not_provisioned",
			"This deployment has no organization yet; seed it before signing in")
	}
	return marker.OrganizationID, nil
}
