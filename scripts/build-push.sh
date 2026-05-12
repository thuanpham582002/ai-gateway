#!/bin/bash
set -euo pipefail

REGISTRY="${1:?Usage: $0 <registry> [command]}"
COMMAND="${2:-controller}"
TAG="${3:-latest}"
IMAGE="${REGISTRY}/ai-gateway-${COMMAND}:${TAG}"

echo "Building ${COMMAND} -> ${IMAGE}"
docker build --platform linux/amd64 --build-arg COMMAND_NAME="${COMMAND}" -t "${IMAGE}" .
docker push "${IMAGE}"
echo "Done: ${IMAGE}"
