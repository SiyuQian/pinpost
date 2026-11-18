#!/usr/bin/env bash
# Builds the three Lambda bundles Terraform uploads. Run before `terraform apply`.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p infrastructure/terraform/dist
for c in server scan webhook; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags lambda.norpc -trimpath -o infrastructure/terraform/dist/bootstrap "./cmd/$c"
  # Fixed mtime keeps the zip byte-identical for an unchanged binary, so
  # source_code_hash doesn't churn on every build.
  touch -t 202001010000 infrastructure/terraform/dist/bootstrap
  (cd infrastructure/terraform/dist && zip -qX "$c.zip" bootstrap)
done
rm infrastructure/terraform/dist/bootstrap
