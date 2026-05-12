#!/bin/bash
set -euo pipefail

REGISTRY="${1:?Usage: $0 <registry> [command] [tag]   (set BUILD_HOST=user@host to rsync+build remotely; REMOTE_DIR overrides path)}"
COMMAND="${2:-controller}"
TAG="${3:-latest}"
IMAGE="${REGISTRY}/ai-gateway-${COMMAND}:${TAG}"

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_NAME="$(basename "${REPO_DIR}")"
GO_IMAGE="${GO_IMAGE:-golang:1.26}"

# Build steps (no host Go required — compile inside a Go container):
#   1. force-remove stale out/<command>-linux-amd64
#   2. compile binary inside ${GO_IMAGE}
#   3. docker build using the existing Dockerfile (it copies out/<command>-linux-amd64 into the image)
#   4. docker push
BUILD_CMD=$(cat <<EOF
set -euo pipefail
rm -f out/${COMMAND}-linux-amd64
mkdir -p out
docker run --rm \\
  -v "\$(pwd):/src" -w /src \\
  -e GOOS=linux -e GOARCH=amd64 -e CGO_ENABLED=0 \\
  ${GO_IMAGE} \\
  go build -trimpath -ldflags='-s -w' -o out/${COMMAND}-linux-amd64 ./cmd/${COMMAND}
ls -la out/${COMMAND}-linux-amd64
docker build --platform linux/amd64 --build-arg COMMAND_NAME=${COMMAND} -t ${IMAGE} .
docker push ${IMAGE}
EOF
)

if [[ -n "${BUILD_HOST:-}" ]]; then
  REMOTE_DIR="${REMOTE_DIR:-/root/workspace/thuanpt}"
  echo "Syncing ${REPO_DIR} -> ${BUILD_HOST}:${REMOTE_DIR}/${REPO_NAME}"
  rsync -rahP \
    --exclude='.git' \
    --exclude='.env*' \
    --exclude='out' \
    --exclude='node_modules' \
    "${REPO_DIR}" "${BUILD_HOST}:${REMOTE_DIR}/"

  echo "Building ${COMMAND} on ${BUILD_HOST} -> ${IMAGE}"
  ssh "${BUILD_HOST}" "cd ${REMOTE_DIR}/${REPO_NAME} && ${BUILD_CMD}"
else
  echo "Building ${COMMAND} -> ${IMAGE}"
  cd "${REPO_DIR}"
  eval "${BUILD_CMD}"
fi

echo "Done: ${IMAGE}"
