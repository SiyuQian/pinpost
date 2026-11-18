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
docker compose up -d --wait && source ./local.env
go vet ./... && go test ./... -count=1
npm --prefix web/admin ci && npm --prefix web/admin run build && npm --prefix web/admin test
npm --prefix sdk ci && npm --prefix sdk test
```

If you change `api/openapi.yaml`, regenerate the server code with
`go generate ./internal/api` and commit the result.

## Licensing of your contribution

Code under `sdk/` is MIT-licensed; everything else is AGPL-3.0-only. Your
contribution is licensed the same way as the directory it lands in.
