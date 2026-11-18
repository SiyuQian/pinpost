package project

import (
	"context"
	"time"

	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

// Service exposes org-scoped project and member reads. Both live in the
// organization's partition, so each listing is one Query.
type Service struct{ Store *platform.Store }

// OrgItem is the organization root record.
type OrgItem struct {
	platform.Key
	OrganizationID string    `dynamodbav:"org_id"`
	Name           string    `dynamodbav:"name"`
	CreatedAt      time.Time `dynamodbav:"created_at"`
}

// Org is the organization root, read for the workspace header.
type Org struct {
	ID   string `json:"ID"`
	Name string `json:"Name"`
}

func (s *Service) Organization(ctx context.Context, orgID string) (Org, error) {
	var it OrgItem
	found, err := s.Store.Get(ctx, "ORG#"+orgID, platform.Meta, &it)
	if err != nil {
		return Org{}, err
	}
	// A member's session pins the org id, so a missing or nameless root record
	// is a half-provisioned tenant, not a bad request — booting the dashboard
	// matters more than the name. It is named by id rather than by a generic
	// literal, so the broken tenant is identifiable from the header instead of
	// looking exactly like a healthy one.
	if !found || it.Name == "" {
		return Org{ID: orgID, Name: "工作区 " + fallbackSuffix(orgID)}, nil
	}
	return Org{ID: it.OrganizationID, Name: it.Name}, nil
}

func fallbackSuffix(orgID string) string {
	if len(orgID) > 8 {
		return orgID[:8]
	}
	return orgID
}

// Project is what the console lists. WriteKey is the public ingestion key;
// empty for projects created before it was stored, until one rotation.
type Project struct {
	ID, Name, Environment string
	WriteKey              string `json:",omitempty"`
}

func (s *Service) ListProjects(ctx context.Context, orgID string) ([]Project, error) {
	var items []auth.ProjectItem
	if err := s.Store.QueryAll(ctx, &dynamodb.QueryInput{
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": platform.S("ORG#" + orgID), ":prefix": platform.S("PROJ#"),
		},
	}, &items); err != nil {
		return nil, err
	}
	out := []Project{}
	for _, p := range items {
		out = append(out, Project{ID: p.ProjectID, Name: p.Name, Environment: p.Environment, WriteKey: p.WriteKey})
	}
	return out, nil
}

// Create provisions a new project inside an existing organization and returns
// it together with the write key. The key is a public identifier, stored
// readable next to its digest so the console can show it again later.
// Environment is empty-optional and defaults to "development".
func (s *Service) Create(ctx context.Context, orgID, name, environment string) (Project, string, error) {
	projID := uuid.Must(uuid.NewV7()).String()
	if environment == "" {
		environment = "development"
	}
	writeKey := auth.NewWriteKey()
	now := time.Now().UTC()
	proj := auth.ProjectItem{
		Key:            auth.ProjectKey(orgID, projID),
		OrganizationID: orgID,
		ProjectID:      projID,
		Name:           name,
		Environment:    environment,
		WriteKeyHash:   auth.HashWriteKey(writeKey),
		WriteKey:       writeKey,
		Settings:       "{}",
		CreatedAt:      now,
	}
	// Second copy keyed by the write-key digest, so ingestion authenticates
	// with one GetItem — the same pair Provision writes for a seeded tenant.
	// Both go in one transaction: committing only the first would publish a
	// project the listing shows but no write key can reach, and nothing can
	// recover the plaintext key to repair it.
	wkCopy := proj
	wkCopy.Key = auth.WriteKeyKey(writeKey)
	if err := s.Store.PutAll(ctx, &proj, &wkCopy); err != nil {
		return Project{}, "", err
	}
	return Project{ID: projID, Name: name, Environment: environment, WriteKey: writeKey}, writeKey, nil
}

// RenameOrg updates the organization root record's name.
func (s *Service) RenameOrg(ctx context.Context, orgID, name string) (Org, error) {
	var it OrgItem
	found, err := s.Store.Get(ctx, "ORG#"+orgID, platform.Meta, &it)
	if err != nil {
		return Org{}, err
	}
	if !found {
		// A half-provisioned tenant has no root record to rename; writing one
		// here would paper over the broken seed Organization() flags.
		return Org{}, platform.Errf(404, "not_found", "Organization not found")
	}
	it.Name = name
	if err := s.Store.Put(ctx, &it); err != nil {
		return Org{}, err
	}
	return Org{ID: it.OrganizationID, Name: it.Name}, nil
}

// Rename updates a project's name on both stored copies — the org-partition
// record that listings read and the WK#<digest> copy ingestion authenticates
// against — so they cannot drift apart.
func (s *Service) Rename(ctx context.Context, orgID, projectID, name string) (Project, error) {
	key := auth.ProjectKey(orgID, projectID)
	var item auth.ProjectItem
	found, err := s.Store.Get(ctx, key.PK, key.SK, &item)
	if err != nil {
		return Project{}, err
	}
	if !found {
		return Project{}, platform.Errf(404, "not_found", "Project not found")
	}
	item.Name = name
	wkCopy := item
	wkCopy.Key = platform.Key{PK: "WK#" + item.WriteKeyHash, SK: platform.Meta}
	if err := s.Store.PutAll(ctx, &item, &wkCopy); err != nil {
		return Project{}, err
	}
	return Project{ID: item.ProjectID, Name: item.Name, Environment: item.Environment, WriteKey: item.WriteKey}, nil
}

// RotateWriteKey replaces a project's write key. The old key stops
// authenticating in the same transaction the new one starts, so there is never
// a moment with zero or two valid keys. Deployed SDKs holding the old key get
// 401 from then on — that is the point of rotating, and the console says so
// before it asks.
//
// ponytail: no grace window. Add an expiring second WK# copy if a customer
// needs a zero-downtime roll.
func (s *Service) RotateWriteKey(ctx context.Context, orgID, projectID string) (Project, error) {
	key := auth.ProjectKey(orgID, projectID)
	var item auth.ProjectItem
	found, err := s.Store.Get(ctx, key.PK, key.SK, &item)
	if err != nil {
		return Project{}, err
	}
	if !found {
		return Project{}, platform.Errf(404, "not_found", "Project not found")
	}
	oldKey := platform.Key{PK: "WK#" + item.WriteKeyHash, SK: platform.Meta}
	writeKey := auth.NewWriteKey()
	item.WriteKey = writeKey
	item.WriteKeyHash = auth.HashWriteKey(writeKey)
	wkCopy := item
	wkCopy.Key = auth.WriteKeyKey(writeKey)
	if err := s.Store.PutAndDelete(ctx, []platform.Key{oldKey}, &item, &wkCopy); err != nil {
		return Project{}, err
	}
	return Project{ID: item.ProjectID, Name: item.Name, Environment: item.Environment, WriteKey: writeKey}, nil
}

type Member struct {
	ID, Email, Name, Role string
	Picture               string `json:",omitempty"`
}

func (s *Service) ListMembers(ctx context.Context, orgID string) ([]Member, error) {
	var items []auth.MemberItem
	if err := s.Store.QueryAll(ctx, &dynamodb.QueryInput{
		KeyConditionExpression:   aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		FilterExpression:         aws.String("#s = :active"),
		ExpressionAttributeNames: map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": platform.S("ORG#" + orgID), ":prefix": platform.S("MEM#"),
			":active": platform.S("active"),
		},
	}, &items); err != nil {
		return nil, err
	}
	out := []Member{}
	for _, m := range items {
		out = append(out, Member{ID: m.MemberID, Email: m.Email, Name: m.Name, Role: m.Role, Picture: m.Picture})
	}
	return out, nil
}

// IsMember reports whether memberID is an active member of the organization.
func (s *Service) IsMember(ctx context.Context, orgID, memberID string) (bool, error) {
	var m auth.MemberItem
	found, err := s.Store.Get(ctx, "ORG#"+orgID, "MEM#"+memberID, &m)
	return found && m.Status == "active", err
}
