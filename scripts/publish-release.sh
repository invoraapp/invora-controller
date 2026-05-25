#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:?version argument required}"
GHCR_IMAGE="${GHCR_IMAGE:-ghcr.io/invoraapp/invora-controller}"
CHART_OCI="${CHART_OCI:-oci://ghcr.io/invoraapp/invora-controller/charts}"

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --push \
  -t "${GHCR_IMAGE}:${VERSION}" \
  -t "${GHCR_IMAGE}:latest" \
  .

helm package charts/invora-controller --destination dist
helm push "dist/invora-controller-${VERSION}.tgz" "${CHART_OCI}"
