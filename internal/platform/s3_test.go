package platform

import (
	"context"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Inside docker compose the server reaches object storage at a name browsers
// cannot resolve; the URLs it hands them must carry the public address.
func TestPresignUsesThePublicEndpoint(t *testing.T) {
	t.Setenv("S3_ENDPOINT_URL", "http://s3:9000")
	t.Setenv("S3_PUBLIC_ENDPOINT_URL", "http://files.example.com:9000")
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("k", "s", "")}
	signed, err := NewPresign(NewS3(cfg)).PresignPutObject(context.Background(),
		&s3.PutObjectInput{Bucket: aws.String("b"), Key: aws.String("p/a")})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(signed.URL)
	if u.Host != "files.example.com:9000" || u.Path != "/b/p/a" {
		t.Fatalf("presigned URL %s, want the public host, path-style", signed.URL)
	}
}
