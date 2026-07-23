#!/usr/bin/env bash

set -euo pipefail

repo_dir=$(cd "$(dirname "$0")/.." && pwd)
KUBE_CONTEXT="${KUBE_CONTEXT:-ai-infer-factory}"
HELM_NAMESPACE="${HELM_NAMESPACE:-envoy-ai-gateway-system}"
HELM_RELEASE="${HELM_RELEASE:-aieg}"
HELM_CHART="${HELM_CHART:-$repo_dir/manifests/charts/ai-gateway-helm}"
HELM_VALUES="${HELM_VALUES:-$repo_dir/deploy/ai-infer-factory/ai-gateway-values.yaml}"
DATA_PLANE_NAMESPACE="${DATA_PLANE_NAMESPACE:-envoy-gateway-system}"
GATEWAY_DEPLOYMENTS="${GATEWAY_DEPLOYMENTS:-envoy-envoy-ai-gateway-system-ai-gateway-e09f2496 envoy-envoy-ai-gateway-system-maas-gateway-dc3b9c84}"
REGISTRY="${REGISTRY:-10.24.10.16:5000}"
VERSION_STRING="${VERSION_STRING:-dev}"
TAG="${TAG:-audit-events-${VERSION_STRING}-$(date -u +%Y%m%d-%H%M%S)}"
IMAGE="${REGISTRY}/ai-gateway-extproc:${TAG}"

deployed=false
previous_revision=""

rollback() {
  local exit_code=$?
  trap - EXIT INT TERM
  if ((exit_code != 0)) && [[ "$deployed" == true && -n "$previous_revision" ]]; then
    echo "acceptance failed; rolling Helm release back to revision $previous_revision" >&2
    helm --kube-context "$KUBE_CONTEXT" -n "$HELM_NAMESPACE" rollback "$HELM_RELEASE" "$previous_revision" \
      --wait --cleanup-on-fail --timeout 5m || true
  fi
  exit "$exit_code"
}
trap rollback EXIT
trap 'exit 130' INT TERM

gateway_name_for_deployment() {
  sed -E 's/^envoy-envoy-ai-gateway-system-(.*)-[a-f0-9]{8}$/\1/' <<<"$1"
}

wait_for_extproc_image() {
  local deployment=$1 expected_image=$2
  local gateway_name expected_replicas deadline ready_count
  gateway_name=$(gateway_name_for_deployment "$deployment")
  expected_replicas=$(kubectl --context "$KUBE_CONTEXT" -n "$DATA_PLANE_NAMESPACE" get deployment "$deployment" -o jsonpath='{.spec.replicas}')
  deadline=$((SECONDS + 300))
  while ((SECONDS < deadline)); do
    ready_count=$(kubectl --context "$KUBE_CONTEXT" -n "$DATA_PLANE_NAMESPACE" get pods \
      -l "gateway.envoyproxy.io/owning-gateway-name=${gateway_name}" -o json | jq \
      --arg image "$expected_image" '[
        .items[] |
        select(any(((.spec.initContainers // []) + (.spec.containers // []))[];
          .name == "ai-gateway-extproc" and .image == $image)) |
        select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))
      ] | length')
    if [[ "$ready_count" == "$expected_replicas" ]]; then
      kubectl --context "$KUBE_CONTEXT" -n "$DATA_PLANE_NAMESPACE" rollout status deployment "$deployment" --timeout=60s
      return 0
    fi
    sleep 5
  done
  echo "timed out waiting for $expected_replicas Ready $gateway_name pods with image $expected_image" >&2
  return 1
}

for required in GATEWAY_URL MODEL; do
  [[ -n "${!required:-}" ]] || { echo "set $required before deploying" >&2; exit 1; }
done

cd "$repo_dir"
previous_revision=$(helm --kube-context "$KUBE_CONTEXT" -n "$HELM_NAMESPACE" history "$HELM_RELEASE" -o json | \
  jq -r '[.[] | select(.status == "deployed")][-1].revision')
make docker-build.extproc \
  VERSION_STRING="$VERSION_STRING" \
  OCI_REGISTRY="$REGISTRY" \
  TAG="$TAG" \
  DOCKER_BUILD_ARGS='--push'

helm --kube-context "$KUBE_CONTEXT" -n "$HELM_NAMESPACE" upgrade "$HELM_RELEASE" "$HELM_CHART" \
  --values "$HELM_VALUES" \
  --set-string "extProc.image.repository=${REGISTRY}/ai-gateway-extproc" \
  --set-string "extProc.image.tag=${TAG}" \
  --atomic --wait --timeout 5m --history-max 10
deployed=true
for deployment in $GATEWAY_DEPLOYMENTS; do
  wait_for_extproc_image "$deployment" "$IMAGE"
done

KUBE_CONTEXT="$KUBE_CONTEXT" \
GATEWAY_URL="$GATEWAY_URL" \
MODEL="$MODEL" \
EXPECTED_RESPONSE_SUBSTRING="${EXPECTED_RESPONSE_SUBSTRING:-}" \
AUTHORIZATION="${AUTHORIZATION:-}" \
"$repo_dir/scripts/kafka-audit-acceptance.sh"

deployed=false
echo "deployed_image=$IMAGE"
