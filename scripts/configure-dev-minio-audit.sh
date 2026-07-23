#!/usr/bin/env bash

set -euo pipefail

for command in kubectl jq openssl base64; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done

repo_dir=$(cd "$(dirname "$0")/.." && pwd)
KUBE_CONTEXT="${KUBE_CONTEXT:-ai-infer-factory}"
MINIO_NAMESPACE="${MINIO_NAMESPACE:-minio}"
MINIO_DEPLOYMENT="${MINIO_DEPLOYMENT:-minio}"
MINIO_ENDPOINT="${MINIO_ENDPOINT:-http://minio.minio.svc.cluster.local:9000}"
GATEWAY_NAMESPACE="${GATEWAY_NAMESPACE:-envoy-ai-gateway-system}"
DATA_PLANE_NAMESPACE="${DATA_PLANE_NAMESPACE:-envoy-gateway-system}"
GATEWAYS="${GATEWAYS:-ai-gateway maas-gateway}"
BUCKET="${BUCKET:-ai-gateway-audit-dev}"
ACCESS_KEY="${ACCESS_KEY:-ai-gateway-audit-dev}"
RETENTION_DAYS="${RETENTION_DAYS:-7}"
SECRET_NAME="${SECRET_NAME:-ai-gateway-audit-s3}"
POLICY_NAME="${POLICY_NAME:-ai-gateway-audit-dev}"

minio_deployment=$(kubectl --context "$KUBE_CONTEXT" -n "$MINIO_NAMESPACE" get deployment "$MINIO_DEPLOYMENT" -o json)
root_user=$(jq -r '.spec.template.spec.containers[] | select(.name == "minio") | .env[] | select(.name == "MINIO_ROOT_USER") | .value' <<<"$minio_deployment")
root_password=$(jq -r '.spec.template.spec.containers[] | select(.name == "minio") | .env[] | select(.name == "MINIO_ROOT_PASSWORD") | .value' <<<"$minio_deployment")
[[ -n "$root_user" && "$root_user" != null && -n "$root_password" && "$root_password" != null ]] || {
  echo "MinIO root credentials were not found in deployment environment variables" >&2
  exit 1
}

if kubectl --context "$KUBE_CONTEXT" -n "$DATA_PLANE_NAMESPACE" get secret "$SECRET_NAME" >/dev/null 2>&1; then
  secret_key=$(kubectl --context "$KUBE_CONTEXT" -n "$DATA_PLANE_NAMESPACE" get secret "$SECRET_NAME" -o jsonpath='{.data.secret-access-key}' | base64 -d)
else
  secret_key=$(openssl rand -hex 24)
fi

policy=$(jq -nc --arg bucket "$BUCKET" '{
  Version: "2012-10-17",
  Statement: [
    {
      Effect: "Allow",
      Action: ["s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"],
      Resource: ("arn:aws:s3:::" + $bucket)
    },
    {
      Effect: "Allow",
      Action: ["s3:PutObject", "s3:GetObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
      Resource: ("arn:aws:s3:::" + $bucket + "/*")
    }
  ]
}')

bootstrap_pod="minio-audit-bootstrap-$(date +%s)"
trap 'kubectl --context "$KUBE_CONTEXT" -n "$MINIO_NAMESPACE" delete pod "$bootstrap_pod" --ignore-not-found --wait=false >/dev/null 2>&1 || true' EXIT
kubectl --context "$KUBE_CONTEXT" -n "$MINIO_NAMESPACE" run "$bootstrap_pod" \
  --restart=Never --image=quay.io/minio/mc:latest \
  --env="MINIO_ENDPOINT=$MINIO_ENDPOINT" --env="MINIO_ROOT_USER=$root_user" --env="MINIO_ROOT_PASSWORD=$root_password" \
  --env="BUCKET=$BUCKET" --env="ACCESS_KEY=$ACCESS_KEY" --env="SECRET_KEY=$secret_key" \
  --env="POLICY_NAME=$POLICY_NAME" --env="POLICY_JSON=$policy" --env="RETENTION_DAYS=$RETENTION_DAYS" \
  --command -- sh -ec '
    mc alias set local "$MINIO_ENDPOINT" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"
    if ! mc stat "local/$BUCKET" >/dev/null 2>&1; then
      mc mb "local/$BUCKET"
      mc version enable "local/$BUCKET"
      mc ilm rule add --expire-days "$RETENTION_DAYS" "local/$BUCKET"
    fi
    printf "%s" "$POLICY_JSON" >/tmp/policy.json
    mc admin policy create local "$POLICY_NAME" /tmp/policy.json
    mc admin user add local "$ACCESS_KEY" "$SECRET_KEY" || {
      mc admin user remove local "$ACCESS_KEY"
      mc admin user add local "$ACCESS_KEY" "$SECRET_KEY"
    }
    mc admin policy attach local "$POLICY_NAME" --user "$ACCESS_KEY"
  ' >/dev/null
kubectl --context "$KUBE_CONTEXT" -n "$MINIO_NAMESPACE" wait --for=jsonpath='{.status.phase}'=Succeeded "pod/$bootstrap_pod" --timeout=120s >/dev/null || {
  kubectl --context "$KUBE_CONTEXT" -n "$MINIO_NAMESPACE" logs "$bootstrap_pod" >&2 || true
  exit 1
}

kubectl --context "$KUBE_CONTEXT" -n "$DATA_PLANE_NAMESPACE" create secret generic "$SECRET_NAME" \
  --from-literal=access-key-id="$ACCESS_KEY" --from-literal=secret-access-key="$secret_key" \
  --dry-run=client -o yaml | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null
kubectl --context "$KUBE_CONTEXT" apply -f "$repo_dir/deploy/ai-infer-factory/audit-s3-gateway-config.yaml" >/dev/null
for gateway in $GATEWAYS; do
  kubectl --context "$KUBE_CONTEXT" -n "$GATEWAY_NAMESPACE" annotate gateway "$gateway" \
    aigateway.envoyproxy.io/gateway-config=audit-s3 --overwrite >/dev/null
done

echo "bucket=$BUCKET"
echo "gateway_config=audit-s3"
echo "credential_secret=$DATA_PLANE_NAMESPACE/$SECRET_NAME"
