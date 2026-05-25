#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:?version argument required}"
GHCR_IMAGE="${GHCR_IMAGE:-ghcr.io/invoraapp/invora-controller}"

rsync -a --delete config/crd/bases/ charts/invora-controller/crds/

mkdir -p dist
tar -czf "dist/invora-controller-crds-${VERSION}.tar.gz" -C config/crd/bases .
cat config/crd/bases/*.yaml > "dist/invora-controller-crds-${VERSION}.yaml"

CHART_DIR="charts/invora-controller"
sed -i "s/^version:.*/version: ${VERSION}/" "${CHART_DIR}/Chart.yaml"
sed -i "s/^appVersion:.*/appVersion: \"${VERSION}\"/" "${CHART_DIR}/Chart.yaml"
sed -i "s|^\([[:space:]]*repository:\).*|\1 ${GHCR_IMAGE}|" "${CHART_DIR}/values.yaml"
sed -i "s|^\([[:space:]]*tag:\).*|\1 \"${VERSION}\"|" "${CHART_DIR}/values.yaml"
