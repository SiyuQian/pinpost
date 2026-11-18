package platform

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Store is the single-table DynamoDB access layer shared by every slice.
// Layout: pk/sk primary key, one sparse index GSI1 (gsi1pk/gsi1sk), and an
// `expires_at` attribute DynamoDB uses to expire sessions, idempotency keys, rate
// limit counters, unclaimed attachments and delivery records.
type Store struct {
	DB    *dynamodb.Client
	Table string
}

// Key is embedded by every item struct.
type Key struct {
	PK string `dynamodbav:"pk"`
	SK string `dynamodbav:"sk"`
}

// Meta is the sort key for entities that have exactly one item per partition.
const Meta = "#META"

// BootstrapMarker is the partition key of the one item whose existence means
// "this deployment has been seeded". It also carries the seeded organization's
// id, so a first-time Google sign-in knows which organization to join.
const BootstrapMarker = "#BOOTSTRAP"

// AWSConfig loads credentials/region and honours AWS_ENDPOINT_URL so the whole
// stack can point at DynamoDB Local and RustFS without any code branch.
func AWSConfig(ctx context.Context) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if ep := os.Getenv("AWS_ENDPOINT_URL"); ep != "" {
		opts = append(opts, awsconfig.WithBaseEndpoint(ep))
	}
	if os.Getenv("AWS_REGION") == "" {
		opts = append(opts, awsconfig.WithRegion("us-east-1"))
	}
	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

func NewStore(cfg aws.Config, table string) *Store {
	return &Store{DB: dynamodb.NewFromConfig(cfg), Table: table}
}

// EnsureTable creates the table, with GSI1, when the table does not exist. It
// does not repair an existing table: a table left over from an older schema is
// left alone, and it never enables the expires_at TTL, so locally nothing
// expires. Local development and the tests call this instead of running
// migrations.
func EnsureTable(ctx context.Context, s *Store) error {
	_, err := s.DB.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &s.Table})
	if err == nil {
		return nil
	}
	var notFound *types.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return err
	}
	_, err = s.DB.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   &s.Table,
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{
			IndexName: aws.String("GSI1"),
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String("gsi1pk"), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String("gsi1sk"), KeyType: types.KeyTypeRange},
			},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}},
	})
	var inUse *types.ResourceInUseException
	if errors.As(err, &inUse) {
		return nil // raced with another process
	}
	return err
}

// ---- item helpers ----

func KeyAV(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: pk},
		"sk": &types.AttributeValueMemberS{Value: sk},
	}
}

func S(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
func N(v string) types.AttributeValue { return &types.AttributeValueMemberN{Value: v} }

// TTL renders an absolute expiry as the epoch-seconds value DynamoDB expects.
func TTL(d time.Duration) int64 { return time.Now().Add(d).Unix() }

// Get loads one item into out. Reports whether the item exists.
func (s *Store) Get(ctx context.Context, pk, sk string, out any) (bool, error) {
	res, err := s.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.Table, Key: KeyAV(pk, sk)})
	if err != nil || res.Item == nil {
		return false, err
	}
	return true, attributevalue.UnmarshalMap(res.Item, out)
}

// Put writes one item, marshalled from a struct.
func (s *Store) Put(ctx context.Context, item any) error {
	av, err := Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.Table, Item: av})
	return err
}

// PutAll writes several items as one all-or-nothing TransactWriteItems.
// Entities stored as more than one item — a project's org-partition record and
// its WK#<digest> ingestion copy — must be committed together: a Put-then-Put
// sequence that fails halfway publishes a project the listing shows but no
// write key can reach, and the store has no way to tell it apart from a
// healthy one afterwards. Every multi-item write goes through here.
//
// ponytail: DynamoDB caps a transaction at 100 items; every caller here writes
// a handful. Split into batches if one ever needs more.
func (s *Store) PutAll(ctx context.Context, items ...any) error {
	return s.PutAndDelete(ctx, nil, items...)
}

// PutAndDelete is PutAll plus deletes in the same transaction — how a write
// key rotates: the old WK#<digest> copy must vanish in the same commit that
// publishes the new one, or a failure halfway leaves two keys accepting
// ingestion for one project.
func (s *Store) PutAndDelete(ctx context.Context, deletes []Key, items ...any) error {
	tx := make([]types.TransactWriteItem, 0, len(items)+len(deletes))
	for _, item := range items {
		av, err := Marshal(item)
		if err != nil {
			return err
		}
		tx = append(tx, types.TransactWriteItem{
			Put: &types.Put{TableName: &s.Table, Item: av},
		})
	}
	for _, k := range deletes {
		tx = append(tx, types.TransactWriteItem{
			Delete: &types.Delete{TableName: &s.Table, Key: KeyAV(k.PK, k.SK)},
		})
	}
	_, err := s.DB.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: tx})
	return err
}

func Marshal(item any) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(item)
}

// QueryAll pages a Query to exhaustion and unmarshals into out (a *[]T).
func (s *Store) QueryAll(ctx context.Context, in *dynamodb.QueryInput, out any) error {
	in.TableName = &s.Table
	var items []map[string]types.AttributeValue
	for {
		res, err := s.DB.Query(ctx, in)
		if err != nil {
			return err
		}
		items = append(items, res.Items...)
		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		in.ExclusiveStartKey = res.LastEvaluatedKey
	}
	return attributevalue.UnmarshalListOfMaps(items, out)
}

// ConditionFailed reports whether err is a rejected ConditionExpression.
func ConditionFailed(err error) bool {
	var cc *types.ConditionalCheckFailedException
	return errors.As(err, &cc)
}

// EnsureBucket creates the attachment bucket if it is missing; this is for the
// local RustFS container.
func EnsureBucket(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket})
	var owned *s3types.BucketAlreadyOwnedByYou
	var exists *s3types.BucketAlreadyExists
	if errors.As(err, &owned) || errors.As(err, &exists) {
		return nil
	}
	return err
}

// NewS3 builds the object-storage client. S3_ENDPOINT_URL points it at RustFS
// for local development, which only speaks path-style addressing.
func NewS3(cfg aws.Config) *s3.Client {
	endpoint := os.Getenv("S3_ENDPOINT_URL")
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		// Presigned URLs are handed to browsers, which will not send the
		// checksum headers the SDK would otherwise sign — the signature then
		// fails to match. Only compute checksums where the API requires them.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
			o.UsePathStyle = true
		}
	})
}
