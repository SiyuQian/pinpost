# Contributing to Pinpost

Thanks for helping. Bug reports, fixes and documentation improvements are all
welcome; for a larger change, open an issue first so we can agree on the
approach before you build it.

## Contributor License Agreement

Before we can merge a pull request, you need to sign the
[Contributor License Agreement](CLA.md). A bot comments on your first pull
request with a link; signing once covers all of your future contributions.

The CLA is why we can offer Pinpost under the AGPL and also sell commercial
licenses. You keep the copyright in your work.

## Development

The [README quick start](README.md#quick-start) runs the whole stack locally
with Docker. Before you open a pull request, run the same checks as CI:

```bash
docker compose up -d --wait postgres s3 && source ./local.env
go vet ./... && go test ./... -count=1
npm --prefix web/admin ci && npm --prefix web/admin run build && npm --prefix web/admin test
```

If you change `api/openapi.yaml`, regenerate the server code with
`go generate ./internal/api` and commit the result.

Schema changes go in a new file under `internal/platform/migrations/`; never
edit one that has been merged, because deployments have already applied it.
Keep each change forward-compatible (add, then use, then drop in a later
release) so a rolling deploy can run old and new code against one schema.

Every query on organization data filters on the caller's organization, and
the database cannot check that for reads. When you add an endpoint that reads
something by id, add a cross-organization case to `cmd/server/authz_test.go`.

## Licensing of your contribution

Contributions to this repository are licensed under [AGPL-3.0-only](LICENSE).
For the MIT-licensed client libraries, contribute to
[Pinpost SDK](https://github.com/SiyuQian/pinpost-sdk).
