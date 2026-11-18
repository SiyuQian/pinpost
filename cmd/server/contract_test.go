package main

// Contract test: exercises the real HTTP API against a live DynamoDB Local and
// MinIO, and validates every response against api/openapi.yaml. Skips when
// DynamoDB is unreachable (set AWS_ENDPOINT_URL; defaults to docker compose).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/SiyuQian/pinpost/internal/api"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/project"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/google/uuid"
)

type contractClient struct {
	t      *testing.T
	ts     *httptest.Server
	doc    *openapi3.T
	router routers.Router
	http   *http.Client
	store  *platform.Store
	srv    *api.Server
	bucket string
	// covered maps "METHOD /spec/path" to the set of status codes the suite
	// actually observed from that operation. Recording the codes — not just
	// that the operation was called — is what lets assertFullCoverage catch a
	// deleted guard: an unexercised 403 is an untested authorization check.
	covered map[string]map[int]bool
}

// testBucket is created in MinIO on first use; DynamoDB Local and MinIO both
// come from docker compose.
const testBucket = "pinpost-test"

func newContractClient(t *testing.T) *contractClient {
	ctx := context.Background()
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		os.Setenv("AWS_ENDPOINT_URL", "http://localhost:8000")
	}
	os.Setenv("AWS_ACCESS_KEY_ID", env("AWS_ACCESS_KEY_ID", "pinpost"))
	// The limiter now counts in DynamoDB, so its window outlives the process
	// and is shared by every test: give the suite room and probe the limit
	// itself on a throwaway tenant below.
	os.Setenv("PINPOST_RATE_LIMIT", env("PINPOST_RATE_LIMIT", "100"))
	os.Setenv("AWS_SECRET_ACCESS_KEY", env("AWS_SECRET_ACCESS_KEY", "pinpost-secret"))

	cfg, err := platform.AWSConfig(ctx)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	store := platform.NewStore(cfg, env("PINPOST_TABLE", "pinpost-test"))
	if err := platform.EnsureTable(ctx, store); err != nil {
		t.Skipf("dynamodb unreachable, skipping contract test: %v", err)
	}
	if err := project.SeedDemo(ctx, store); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Object storage lives on its own endpoint (MinIO in docker compose).
	if os.Getenv("S3_ENDPOINT_URL") == "" {
		os.Setenv("S3_ENDPOINT_URL", "http://localhost:9000")
	}
	s3c := platform.NewS3(cfg)
	if _, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: ptr(testBucket)}); err != nil {
		var owned *s3types.BucketAlreadyOwnedByYou
		var exists *s3types.BucketAlreadyExists
		if !errors.As(err, &owned) && !errors.As(err, &exists) {
			t.Skipf("object storage unreachable, skipping contract test: %v", err)
		}
	}

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("spec invalid: %v", err)
	}

	srv := newServer(store, s3c, testBucket)
	ts := httptest.NewServer(newMux(srv))
	t.Cleanup(ts.Close)
	doc.Servers = openapi3.Servers{{URL: ts.URL}}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatalf("router: %v", err)
	}

	jar, _ := cookiejar.New(nil)
	return &contractClient{t: t, ts: ts, doc: doc, router: router,
		http: &http.Client{Jar: jar,
			// The download endpoint answers 302 to a presigned URL; the test
			// asserts on the redirect itself.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		store: store, srv: srv, bucket: testBucket, covered: map[string]map[int]bool{}}
}

func ptr[T any](v T) *T { return &v }

// putObject uploads bytes to the presigned URL, exactly as an SDK would.
func (c *contractClient) putObject(uploadURL string, body []byte) int {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("upload: %v", err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// verify runs the scan handler's body against the object just uploaded. The
// deployed stack calls it from an S3 event; tests call it directly rather than
// simulating the event system.
func (c *contractClient) verify(uploadURL string, size int) {
	c.t.Helper()
	u, err := url.Parse(uploadURL)
	if err != nil {
		c.t.Fatal(err)
	}
	key := strings.TrimPrefix(u.Path, "/"+c.bucket+"/")
	if err := c.srv.Attachment.Verify(context.Background(), key, int64(size)); err != nil {
		c.t.Fatalf("verify %s: %v", key, err)
	}
}

// upload authorizes, uploads and verifies one attachment, returning its id.
func (c *contractClient) upload(writeKey map[string]string, name, mime string, body []byte) string {
	c.t.Helper()
	_, res := c.do("POST", "/v1/attachment-uploads", writeKey,
		[]byte(fmt.Sprintf(`{"original_name":%q,"mime_type":%q,"size":%d}`, name, mime, len(body))))
	uploadURL := jsonField(c.t, res, "upload_url")
	if !strings.HasPrefix(uploadURL, "http") {
		c.t.Fatalf("authorize upload %s: %s", name, res)
	}
	if s := c.putObject(uploadURL, body); s != 200 {
		c.t.Fatalf("upload %s: %d", name, s)
	}
	c.verify(uploadURL, len(body))
	return jsonField(c.t, res, "attachment_id")
}

// do sends the request, validating both the request and the response against the spec.
func (c *contractClient) do(method, path string, headers map[string]string, body []byte) (int, []byte) {
	c.t.Helper()
	return c.doOpt(method, path, headers, body, true)
}

// doInvalid sends a deliberately spec-violating request (to probe error
// responses) and validates only the response.
func (c *contractClient) doInvalid(method, path string, headers map[string]string, body []byte) (int, []byte) {
	c.t.Helper()
	return c.doOpt(method, path, headers, body, false)
}

func (c *contractClient) doOpt(method, path string, headers map[string]string, body []byte, validateRequest bool) (int, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.ts.URL+path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" && len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	route, pathParams, err := c.router.FindRoute(req)
	if err != nil {
		c.t.Fatalf("%s %s not in spec: %v", method, path, err)
	}
	op := method + " " + route.Path
	if c.covered[op] == nil {
		c.covered[op] = map[int]bool{}
	}

	reqInput := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if validateRequest {
		if err := openapi3filter.ValidateRequest(context.Background(), reqInput); err != nil {
			c.t.Errorf("%s %s request violates spec: %v", method, path, err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body)) // ValidateRequest may consume the body
	}

	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(res.Body)
	c.covered[op][res.StatusCode] = true

	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqInput,
		Status:                 res.StatusCode,
		Header:                 res.Header,
		Body:                   io.NopCloser(bytes.NewReader(respBody)),
	}
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		c.t.Errorf("%s %s → %d violates spec: %v\nbody: %s", method, path, res.StatusCode, err, respBody)
	}
	return res.StatusCode, respBody
}

// assertFullCoverage fails unless every operation in the spec was exercised.
// This is the guard that keeps the backend spec-driven: a new endpoint without
// spec + contract coverage turns this red.
func (c *contractClient) assertFullCoverage() {
	c.t.Helper()
	for path, item := range c.doc.Paths.Map() {
		for method, op := range item.Operations() {
			seen, ok := c.covered[method+" "+path]
			if !ok {
				c.t.Errorf("spec operation %s %s was never exercised by the contract test", method, path)
				continue
			}
			for code := range op.Responses.Map() {
				status, err := strconv.Atoi(code)
				if err != nil || !enforcedStatus(status) || seen[status] ||
					unreachableHere[method+" "+path+" "+code] {
					continue
				}
				c.t.Errorf("spec operation %s %s declares %d but the contract test never observed it", method, path, status)
			}
		}
	}
}

// enforcedStatus reports whether assertFullCoverage insists a declared status
// code was actually observed. Asserting an operation was merely *called* is
// blind to its guards: deleting requireAdmin from an endpoint left every test
// green because no 403 was ever demanded. Authorization (403) and validation
// (422) are the codes the handlers implement themselves and can silently lose,
// so those are pinned. The rest are declared but not enforced here: 401 is the
// shared session middleware (covered once by TestUnauthenticated), and 404/409/
// 429/5xx depend on state or upstreams a single pass cannot reach for every
// operation. Widen this as those become reachable.
func enforcedStatus(status int) bool { return status == 403 || status == 422 }

// unreachableHere names the operation/status pairs enforcedStatus would demand
// that this suite genuinely cannot reach, each with the test that does cover
// it. Naming them one by one keeps the guard honest: a newly added endpoint
// cannot slip through by accident, only by an explicit entry here.
var unreachableHere = map[string]bool{
	// Needs a configured Google OAuth client and a signed credential; the
	// suite runs with none, so this endpoint answers 404 here. The 403 paths
	// (unknown account, deactivated member) are covered by
	// internal/auth/google_store_test.go and google_test.go.
	"POST /v1/auth/google 403": true,
}

func jsonField(t *testing.T, body []byte, path ...string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	var cur any = m
	for _, k := range path {
		cur = cur.(map[string]any)[k]
	}
	return fmt.Sprint(cur)
}

func TestAPIContract(t *testing.T) {
	c := newContractClient(t)
	wk := map[string]string{"X-Pinpost-Write-Key": project.DemoWriteKey}

	// Ingestion: config, feedback create + idempotent replay, validation error.
	c.do("GET", "/v1/sdk/config", wk, nil)

	idem := uuid.NewString()
	fb := []byte(`{"type":"bug","message":"contract test feedback","rating":3,"context":{"platform":"web"}}`)
	status, body := c.do("POST", "/v1/feedback", withIdem(wk, idem), fb)
	if status != 201 {
		t.Fatalf("create feedback: %d %s", status, body)
	}
	fbID := jsonField(t, body, "feedback_id")
	status2, body2 := c.do("POST", "/v1/feedback", withIdem(wk, idem), fb)
	if status2 != 201 || jsonField(t, body2, "feedback_id") != fbID {
		t.Fatalf("idempotent replay returned different result: %d %s", status2, body2)
	}
	if s, _ := c.doInvalid("POST", "/v1/feedback", withIdem(wk, uuid.NewString()), []byte(`{"type":"nope","message":""}`)); s != 422 {
		t.Fatalf("validation error: want 422, got %d", s)
	}
	if s, _ := c.do("GET", "/v1/sdk/config", map[string]string{"X-Pinpost-Write-Key": "wk_wrong"}, nil); s != 401 {
		t.Fatalf("bad write key: want 401, got %d", s)
	}

	// Attachment: authorize, upload straight to object storage, verify, link.
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 60))
	attID := c.upload(wk, "shot.png", "image/png", png)
	s, b := c.do("POST", "/v1/feedback", withIdem(wk, uuid.NewString()),
		[]byte(fmt.Sprintf(`{"type":"suggestion","message":"with attachment","attachment_ids":[%q]}`, attID)))
	if s != 201 {
		t.Fatalf("feedback with attachment: %d %s", s, b)
	}
	attFbID := jsonField(t, b, "feedback_id")

	// Dashboard: unauthenticated 401, then login and walk the workflow.
	if s, _ := c.doNoCookies("GET", "/v1/feedback"); s != 401 {
		t.Fatalf("unauthenticated list: want 401, got %d", s)
	}
	_, loginBody := c.do("POST", "/v1/auth/login", nil,
		[]byte(fmt.Sprintf(`{"Email":%q,"Password":%q}`, project.DemoEmail, project.DemoPassword)))
	memberID := jsonField(t, loginBody, "member", "ID")

	// The contract test runs without a Google client, so the admin app is told
	// there is none and the endpoint refuses rather than half-verifying.
	if _, b := c.do("GET", "/v1/auth/config", nil, nil); !bytes.Contains(b, []byte(`"google_client_id":""`)) {
		t.Fatalf("auth config: %s", b)
	}
	if s, _ := c.do("POST", "/v1/auth/google", nil, []byte(`{"Credential":"not-a-token"}`)); s != 404 {
		t.Fatalf("google login with no client configured: want 404, got %d", s)
	}

	c.do("GET", "/v1/me", nil, nil)
	_, projBody := c.do("GET", "/v1/projects", nil, nil)
	// Rename the project and read it back, so the listing reflects the change.
	var projList struct{ Projects []struct{ ID string } }
	if err := json.Unmarshal(projBody, &projList); err != nil || len(projList.Projects) == 0 {
		t.Fatalf("projects listing: %v %s", err, projBody)
	}
	projID := projList.Projects[0].ID
	if s, b := c.do("PATCH", "/v1/projects/"+projID, nil, []byte(`{"Name":"Acme App"}`)); s != 200 ||
		!bytes.Contains(b, []byte(`"Acme App"`)) {
		t.Fatalf("rename project: %d %s", s, b)
	}
	if _, b := c.do("GET", "/v1/projects", nil, nil); !bytes.Contains(b, []byte(`"Acme App"`)) {
		t.Fatalf("rename not visible in listing: %s", b)
	}
	if s, _ := c.do("PATCH", "/v1/projects/"+projID, nil, []byte(`{"Name":"  "}`)); s != 422 {
		t.Fatalf("blank rename: want 422, got %d", s)
	}
	// Create a project: the write key is returned exactly once, and the new
	// project shows up in the listing.
	createStatus, createBody := c.do("POST", "/v1/projects", nil, []byte(`{"Name":"New Project"}`))
	if createStatus != 201 {
		t.Fatalf("create project: %d %s", createStatus, createBody)
	}
	newKey := jsonField(t, createBody, "WriteKey")
	if newKey == "" {
		t.Fatal("create project must return the write key")
	}
	if _, b := c.do("GET", "/v1/projects", nil, nil); !bytes.Contains(b, []byte(`"New Project"`)) {
		t.Fatalf("created project not visible in listing: %s", b)
	}
	// The write key is a public identifier, so the listing shows it again —
	// the install page depends on this rather than on the one-time create
	// response.
	if _, b := c.do("GET", "/v1/projects", nil, nil); !bytes.Contains(b, []byte(newKey)) {
		t.Fatalf("write key not visible in listing: %s", b)
	}
	// Rotation: the new key authenticates, the old one is refused, and the
	// listing shows the new one.
	newProjID := jsonField(t, createBody, "ID")
	if s, _ := c.do("GET", "/v1/sdk/config", map[string]string{"X-Pinpost-Write-Key": newKey}, nil); s != 200 {
		t.Fatalf("fresh write key: want 200, got %d", s)
	}
	rotStatus, rotBody := c.do("POST", "/v1/projects/"+newProjID+"/write-key/rotate", nil, nil)
	if rotStatus != 200 {
		t.Fatalf("rotate write key: %d %s", rotStatus, rotBody)
	}
	rotated := jsonField(t, rotBody, "WriteKey")
	if rotated == "" || rotated == newKey {
		t.Fatalf("rotate must issue a different key, got %q", rotated)
	}
	if s, _ := c.do("GET", "/v1/sdk/config", map[string]string{"X-Pinpost-Write-Key": newKey}, nil); s != 401 {
		t.Fatalf("rotated-out write key: want 401, got %d", s)
	}
	if s, _ := c.do("GET", "/v1/sdk/config", map[string]string{"X-Pinpost-Write-Key": rotated}, nil); s != 200 {
		t.Fatalf("rotated-in write key: want 200, got %d", s)
	}
	if _, b := c.do("GET", "/v1/projects", nil, nil); !bytes.Contains(b, []byte(rotated)) || bytes.Contains(b, []byte(newKey)) {
		t.Fatalf("listing after rotate: %s", b)
	}
	if s, _ := c.do("POST", "/v1/projects/"+uuid.NewString()+"/write-key/rotate", nil, nil); s != 404 {
		t.Fatalf("rotate unknown project: want 404, got %d", s)
	}
	// Environment is optional and defaults to "development" — EnvBadge styles
	// on that exact value, so a regression to "" renders a blank badge.
	if env := jsonField(t, createBody, "Environment"); env != "development" {
		t.Fatalf("create project default environment: want development, got %q", env)
	}
	if cs, _ := c.do("POST", "/v1/projects", nil, []byte(`{"Name":"  "}`)); cs != 422 {
		t.Fatalf("blank create: want 422, got %d", cs)
	}
	// The spec's maxLength counts characters, so a 41-character Chinese name
	// (123 UTF-8 bytes) is within the contract and must be accepted — the UI
	// is entirely Chinese, so byte-counting rejects ordinary names.
	cjkName := strings.Repeat("项", 41)
	if cs, cb := c.do("POST", "/v1/projects", nil,
		[]byte(fmt.Sprintf(`{"Name":%q}`, cjkName))); cs != 201 {
		t.Fatalf("41-character CJK name: want 201, got %d %s", cs, cb)
	}
	// Environment is an allowlist matching the two values the console offers;
	// anything else would render with EnvBadge's non-production styling while
	// claiming to be production.
	if cs, _ := c.doInvalid("POST", "/v1/projects", nil,
		[]byte(`{"Name":"Wrong Env","Environment":"Production"}`)); cs != 422 {
		t.Fatalf("non-allowlisted environment: want 422, got %d", cs)
	}
	if s, b := c.do("PATCH", "/v1/organization", nil, []byte(`{"Name":"Acme"}`)); s != 200 ||
		!bytes.Contains(b, []byte(`"Acme"`)) {
		t.Fatalf("rename organization: %d %s", s, b)
	}
	if _, b := c.do("GET", "/v1/me", nil, nil); !bytes.Contains(b, []byte(`"Acme"`)) {
		t.Fatalf("org rename not visible on /v1/me: %s", b)
	}
	if s, _ := c.do("PATCH", "/v1/organization", nil, []byte(`{"Name":"  "}`)); s != 422 {
		t.Fatalf("blank organization rename: want 422, got %d", s)
	}
	// Internal feedback: a member files a report from the dashboard. It is
	// stored with source "internal" and the widget-sourced rows keep reporting
	// "widget", which is what the inbox filter and badge read.
	internalIdem := uuid.NewString()
	internalBody := []byte(fmt.Sprintf(
		`{"project_id":%q,"type":"suggestion","message":"internal contract report","rating":4}`, projID))
	is, ib := c.do("POST", "/v1/internal-feedback", map[string]string{"Idempotency-Key": internalIdem}, internalBody)
	if is != 201 {
		t.Fatalf("create internal feedback: %d %s", is, ib)
	}
	internalID := jsonField(t, ib, "feedback_id")
	if s2, b2 := c.do("POST", "/v1/internal-feedback", map[string]string{"Idempotency-Key": internalIdem}, internalBody); s2 != 201 ||
		jsonField(t, b2, "feedback_id") != internalID {
		t.Fatalf("internal idempotent replay returned different result: %d %s", s2, b2)
	}
	if _, b := c.do("GET", "/v1/feedback/"+internalID, nil, nil); !bytes.Contains(b, []byte(`"source":"internal"`)) {
		t.Fatalf("internal report must read back as source internal: %s", b)
	}
	if _, b := c.do("GET", "/v1/feedback/"+fbID, nil, nil); !bytes.Contains(b, []byte(`"source":"widget"`)) {
		t.Fatalf("ingested report must read back as source widget: %s", b)
	}
	// The filter separates the two, which is the whole point of storing it.
	if _, b := c.do("GET", "/v1/feedback?source=internal", nil, nil); !bytes.Contains(b, []byte(internalID)) ||
		bytes.Contains(b, []byte(fbID)) {
		t.Fatalf("source=internal filter: %s", b)
	}
	if _, b := c.do("GET", "/v1/feedback?source=widget", nil, nil); !bytes.Contains(b, []byte(fbID)) ||
		bytes.Contains(b, []byte(internalID)) {
		t.Fatalf("source=widget filter: %s", b)
	}
	// Provenance is decided by the endpoint, never by the body: a client that
	// posts its own source to public ingestion is still recorded as widget.
	spoofIdem := uuid.NewString()
	_, spoofBody := c.do("POST", "/v1/feedback", withIdem(wk, spoofIdem),
		[]byte(`{"type":"bug","message":"spoof attempt","source":"internal"}`))
	if _, b := c.do("GET", "/v1/feedback/"+jsonField(t, spoofBody, "feedback_id"), nil, nil); !bytes.Contains(b, []byte(`"source":"widget"`)) {
		t.Fatalf("body-supplied source must be ignored: %s", b)
	}
	// A project from outside the caller's organization is refused before write.
	if s2, _ := c.doInvalid("POST", "/v1/internal-feedback", map[string]string{"Idempotency-Key": uuid.NewString()},
		[]byte(fmt.Sprintf(`{"project_id":%q,"type":"bug","message":"foreign project"}`, uuid.NewString()))); s2 != 422 {
		t.Fatalf("internal feedback for an unknown project: want 422, got %d", s2)
	}
	if s2, _ := c.doNoCookies("GET", "/v1/feedback?source=internal"); s2 != 401 {
		t.Fatalf("unauthenticated source filter: want 401, got %d", s2)
	}
	if s2, _ := c.doInvalid("GET", "/v1/feedback?source=nope", nil, nil); s2 != 422 {
		t.Fatalf("invalid source filter: want 422, got %d", s2)
	}

	// A cursor that did not come from a previous page is rejected, not silently
	// treated as "start from the beginning".
	if s, _ := c.do("GET", "/v1/feedback?cursor=not-a-cursor", nil, nil); s != 422 {
		t.Fatalf("bad cursor: want 422, got %d", s)
	}
	c.do("GET", "/v1/members", nil, nil)
	c.do("GET", "/v1/setup", nil, nil)
	c.do("GET", "/v1/feedback?limit=5&type=bug", nil, nil)
	_, detailBody := c.do("GET", "/v1/feedback/"+fbID, nil, nil)
	c.do("GET", "/v1/analytics/overview", nil, nil)
	// The same aggregate scoped to one project — how the dashboard reads it
	// whenever a project is selected in the sidebar.
	c.do("GET", "/v1/analytics/overview?project_id="+jsonField(t, detailBody, "project_id"), nil, nil)
	c.do("GET", "/v1/attachments/"+attID+"/download", nil, nil)

	if s, b := c.do("PATCH", "/v1/feedback/"+fbID, nil,
		[]byte(fmt.Sprintf(`{"status":"in_progress","priority":"high","assignee_id":%q,"tags":["contract"]}`, memberID))); s != 200 {
		t.Fatalf("patch: %d %s", s, b)
	}
	// resolved -> spam is an illegal transition per the state machine.
	c.do("PATCH", "/v1/feedback/"+fbID, nil, []byte(`{"status":"resolved"}`))
	if s, _ := c.do("PATCH", "/v1/feedback/"+fbID, nil, []byte(`{"status":"spam"}`)); s != 422 {
		t.Fatalf("illegal transition: want 422, got %d", s)
	}
	// Stale optimistic-concurrency token.
	if s, _ := c.do("PATCH", "/v1/feedback/"+fbID, nil, []byte(`{"status":"open","updated_at":"2020-01-01T00:00:00Z"}`)); s != 409 {
		t.Fatalf("stale updated_at: want 409, got %d", s)
	}
	c.do("POST", "/v1/feedback/"+fbID+"/notes", nil, []byte(`{"Note":"contract note"}`))
	// A whitespace-only note passes the spec's minLength but is still empty.
	if s, _ := c.do("POST", "/v1/feedback/"+fbID+"/notes", nil, []byte(`{"Note":"   "}`)); s != 422 {
		t.Fatalf("blank note: want 422, got %d", s)
	}
	c.do("GET", "/v1/feedback/"+fbID+"/activities", nil, nil)
	c.do("GET", "/v1/feedback/"+attFbID, nil, nil)
	if s, _ := c.do("GET", "/v1/feedback/"+uuid.NewString(), nil, nil); s != 404 {
		t.Fatalf("missing feedback: want 404, got %d", s)
	}

	// Webhooks: register, list (no secret), pause/resume, deliveries, replay 404, delete.
	_, whBody := c.do("POST", "/v1/webhook-endpoints", nil,
		[]byte(`{"url":"https://example.com/hook","event_types":["feedback.created.v1"]}`))
	whID := jsonField(t, whBody, "id")
	if jsonField(t, whBody, "secret") == "" {
		t.Fatal("webhook create must return the secret")
	}
	if _, lb := c.do("GET", "/v1/webhook-endpoints", nil, nil); strings.Contains(string(lb), "whsec_") {
		t.Fatal("webhook list must not expose secrets")
	}
	if s, _ := c.do("PATCH", "/v1/webhook-endpoints/"+whID, nil, []byte(`{"status":"paused"}`)); s != 200 {
		t.Fatalf("pause webhook: %d", s)
	}
	if s, _ := c.doInvalid("PATCH", "/v1/webhook-endpoints/"+whID, nil, []byte(`{"status":"bogus"}`)); s != 422 {
		t.Fatalf("bad webhook status: want 422, got %d", s)
	}
	c.do("GET", "/v1/webhook-endpoints/"+whID+"/deliveries", nil, nil)
	if s, _ := c.do("POST", "/v1/webhook-deliveries/"+uuid.NewString()+"/replay", nil, nil); s != 404 {
		t.Fatalf("replay unknown delivery: want 404, got %d", s)
	}
	if s, _ := c.doInvalid("POST", "/v1/webhook-endpoints", nil, []byte(`{"url":"not a url"}`)); s != 422 {
		t.Fatalf("bad webhook url: want 422, got %d", s)
	}
	if s, _ := c.do("DELETE", "/v1/webhook-endpoints/"+whID, nil, nil); s != 200 {
		t.Fatalf("delete webhook: %d", s)
	}

	// The presigned URL signs the declared length, so object storage itself
	// rejects a body of any other size — a check the old upload-through-the-API
	// path could not make.
	_, mismatch := c.do("POST", "/v1/attachment-uploads", wk,
		[]byte(`{"original_name":"short.png","mime_type":"image/png","size":128}`))
	if s := c.putObject(jsonField(t, mismatch, "upload_url"), []byte("too short")); s < 400 {
		t.Fatalf("size-mismatched upload: want 4xx, got %d", s)
	}
	// An unsupported media type is refused before anything is presigned.
	if s, _ := c.doInvalid("POST", "/v1/attachment-uploads", wk,
		[]byte(`{"original_name":"payload.exe","mime_type":"application/x-msdownload","size":128}`)); s != 422 {
		t.Fatalf("disallowed mime type: want 422, got %d", s)
	}
	// Download of an authorized-but-never-uploaded attachment is unavailable.
	_, pendingBody := c.do("POST", "/v1/attachment-uploads", wk,
		[]byte(`{"original_name":"pending.png","mime_type":"image/png","size":100}`))
	if s, _ := c.do("GET", "/v1/attachments/"+jsonField(t, pendingBody, "attachment_id")+"/download", nil, nil); s != 409 {
		t.Fatalf("unscanned download: want 409, got %d", s)
	}

	// A viewer role cannot modify feedback.
	viewerEmail := "viewer@example.com"
	orgID := jsonField(t, loginBody, "member", "OrganizationID")
	if _, err := project.AddMember(context.Background(), c.store, orgID,
		viewerEmail, "Contract Viewer", "viewer", "viewer-pass"); err != nil {
		t.Fatal(err)
	}
	c.do("POST", "/v1/auth/logout", nil, nil)
	c.do("POST", "/v1/auth/login", nil,
		[]byte(fmt.Sprintf(`{"Email":%q,"Password":"viewer-pass"}`, viewerEmail)))
	if s, _ := c.do("PATCH", "/v1/feedback/"+fbID, nil, []byte(`{"priority":"low"}`)); s != 403 {
		t.Fatalf("viewer patch: want 403, got %d", s)
	}
	if s, _ := c.do("POST", "/v1/feedback/"+fbID+"/notes", nil, []byte(`{"Note":"nope"}`)); s != 403 {
		t.Fatalf("viewer note: want 403, got %d", s)
	}
	// Filing internal feedback is a write, so a read-only role cannot do it —
	// and the refusal must come before anything is stored.
	viewerMessage := "viewer internal report " + uuid.NewString()
	if s, _ := c.do("POST", "/v1/internal-feedback", map[string]string{"Idempotency-Key": uuid.NewString()},
		[]byte(fmt.Sprintf(`{"project_id":%q,"type":"bug","message":%q}`, projID, viewerMessage))); s != 403 {
		t.Fatalf("viewer internal feedback: want 403, got %d", s)
	}
	// Webhook management is owner/admin only.
	if s, _ := c.do("GET", "/v1/webhook-endpoints", nil, nil); s != 403 {
		t.Fatalf("viewer webhook list: want 403, got %d", s)
	}
	// Every other requireAdmin endpoint refuses a viewer too. These are the
	// guards assertFullCoverage now demands a 403 for: without them, deleting
	// requireAdmin from a handler left the whole suite green.
	someID := uuid.NewString()
	// Unique per run: the local table outlives a single `go test`, so an
	// absence assertion on a fixed name would read a previous run's rows.
	viewerProject := "viewer project " + uuid.NewString()
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/projects", fmt.Sprintf(`{"Name":%q}`, viewerProject)},
		{"PATCH", "/v1/projects/" + projID, `{"Name":"viewer rename"}`},
		{"POST", "/v1/projects/" + projID + "/write-key/rotate", ""},
		{"PATCH", "/v1/organization", `{"Name":"viewer org"}`},
		{"POST", "/v1/webhook-endpoints", `{"url":"https://example.com/hook","event_types":["feedback.created.v1"]}`},
		{"PATCH", "/v1/webhook-endpoints/" + someID, `{"status":"paused"}`},
		{"DELETE", "/v1/webhook-endpoints/" + someID, ""},
		{"GET", "/v1/webhook-endpoints/" + someID + "/deliveries", ""},
		{"POST", "/v1/webhook-deliveries/" + someID + "/replay", ""},
	} {
		var body []byte
		if tc.body != "" {
			body = []byte(tc.body)
		}
		if s, b := c.do(tc.method, tc.path, nil, body); s != 403 {
			t.Errorf("viewer %s %s: want 403, got %d %s", tc.method, tc.path, s, b)
		}
	}
	c.do("POST", "/v1/auth/logout", nil, nil)
	// The refused create must not have written anything: a handler that
	// returns 403 after persisting is the same bug wearing a status code.
	c.do("POST", "/v1/auth/login", nil,
		[]byte(fmt.Sprintf(`{"Email":%q,"Password":"pinpost-demo"}`, project.DemoEmail)))
	if _, b := c.do("GET", "/v1/feedback?limit=100", nil, nil); bytes.Contains(b, []byte(viewerMessage)) {
		t.Errorf("viewer internal feedback was refused but still wrote a record: %s", b)
	}
	if _, b := c.do("GET", "/v1/projects", nil, nil); bytes.Contains(b, []byte(viewerProject)) {
		t.Errorf("viewer create was refused but still wrote a project: %s", b)
	}
	c.do("POST", "/v1/auth/logout", nil, nil)

	// Rate limiting: hammer the cheapest write-key endpoint until 429. The
	// counter is keyed per project, so a throwaway tenant keeps this from
	// starving the demo tenant the rest of the suite uses.
	limitKey, err := project.Provision(context.Background(), c.store, project.Params{
		OrgName: "Rate Limit Probe", ProjectName: "Probe", Environment: "development",
		Email: "probe-" + uuid.NewString() + "@pinpost.test", Password: uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	probe := map[string]string{"X-Pinpost-Write-Key": limitKey}
	limit := ingestLimit()
	got429 := false
	for i := 0; i < limit*2+10 && !got429; i++ {
		s, _ := c.do("GET", "/v1/sdk/config", probe, nil)
		got429 = s == 429
	}
	if !got429 {
		t.Errorf("rate limiter never returned 429 after %d requests", limit*2+10)
	}

	c.assertFullCoverage()
}

func withIdem(base map[string]string, key string) map[string]string {
	out := map[string]string{"Idempotency-Key": key}
	for k, v := range base {
		out[k] = v
	}
	return out
}

// doNoCookies issues a request without the session jar (for 401 checks).
func (c *contractClient) doNoCookies(method, path string) (int, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.ts.URL+path, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, body
}
