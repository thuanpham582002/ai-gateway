#!/usr/bin/env bash

set -euo pipefail

for command in kubectl curl jq awk grep; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done

KUBE_CONTEXT="${KUBE_CONTEXT:-$(kubectl config current-context)}"
KAFKA_NAMESPACE="${KAFKA_NAMESPACE:-kafka}"
KAFKA_POD="${KAFKA_POD:-ai-gateway-kafka-combined-0}"
KAFKA_BOOTSTRAP="${KAFKA_BOOTSTRAP:-ai-gateway-kafka-kafka-bootstrap:9092}"
KAFKA_TOPIC="${KAFKA_TOPIC:-ai-gateway-events}"
GATEWAY_URL="${GATEWAY_URL:?set GATEWAY_URL to the Chat Completions acceptance endpoint}"
MODEL="${MODEL:?set MODEL to the routed model name}"
EXPECTED_RESPONSE_SUBSTRING="${EXPECTED_RESPONSE_SUBSTRING:-}"
ACCEPTANCE_TIMEOUT_SECONDS="${ACCEPTANCE_TIMEOUT_SECONDS:-30}"
SAFE_API_KEY_ID="${SAFE_API_KEY_ID:-audit-safe-key-id}"
SAFE_RESERVED_INPUT_TOKENS="${SAFE_RESERVED_INPUT_TOKENS:-100}"
EXPECT_S3_BODY_OBJECT="${EXPECT_S3_BODY_OBJECT:-false}"
S3_ACCEPTANCE_BODY_BYTES="${S3_ACCEPTANCE_BODY_BYTES:-8192}"

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
marker=$(cat /proc/sys/kernel/random/uuid)

case "$EXPECT_S3_BODY_OBJECT" in
  true|false) ;;
  *) echo "EXPECT_S3_BODY_OBJECT must be true or false" >&2; exit 1 ;;
esac

kafka_offsets() {
  kubectl --context "$KUBE_CONTEXT" -n "$KAFKA_NAMESPACE" exec "$KAFKA_POD" -- \
    bin/kafka-get-offsets.sh --bootstrap-server "$KAFKA_BOOTSTRAP" --topic "$KAFKA_TOPIC" \
    2>/dev/null | sort
}

consume_new_records() {
  local before_file=$1 after_file=$2 output_file=$3
  : >"$output_file"
  while IFS=: read -r _ partition before; do
    local after count
    after=$(awk -F: -v p="$partition" '$2 == p {print $3}' "$after_file")
    count=$((after - before))
    if ((count <= 0)); then
      continue
    fi
    kubectl --context "$KUBE_CONTEXT" -n "$KAFKA_NAMESPACE" exec "$KAFKA_POD" -- \
      bin/kafka-console-consumer.sh \
      --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --topic "$KAFKA_TOPIC" \
      --partition "$partition" \
      --offset "$before" \
      --max-messages "$count" \
      --timeout-ms 15000 \
      --property print.key=true \
      --property key.separator='|' \
      2>/dev/null >>"$output_file"
  done <"$before_file"
}

kafka_offsets >"$work_dir/offsets-before"
padding=""
if [[ "$EXPECT_S3_BODY_OBJECT" == true ]]; then
  printf -v padding '%*s' "$S3_ACCEPTANCE_BODY_BYTES" ''
  padding=${padding// /x}
fi
request_body=$(jq -nc --arg model "$MODEL" --arg marker "$marker" --arg padding "$padding" '{
  model: $model,
  messages: [{role: "user", content: ("Kafka audit acceptance " + $marker + $padding)}],
  max_tokens: 1
}')

curl_args=(
  --silent --show-error --max-time 30
  --dump-header "$work_dir/response.headers"
  --output "$work_dir/response.json"
  --write-out '%{http_code}'
  --header 'Content-Type: application/json'
  --header 'User-Agent: curl/audit-acceptance'
  --header "X-API-Key-ID: $SAFE_API_KEY_ID"
  --header "X-MaaS-Reserved-Input-Tokens: $SAFE_RESERVED_INPUT_TOKENS"
  --data "$request_body"
)
if [[ -n "${AUTHORIZATION:-}" ]]; then
  curl_args+=(--header "Authorization: $AUTHORIZATION")
fi

http_code=$(curl "${curl_args[@]}" "$GATEWAY_URL")
[[ "$http_code" == "200" ]] || {
  echo "acceptance request failed with HTTP $http_code" >&2
  cat "$work_dir/response.json" >&2
  exit 1
}
if [[ -n "$EXPECTED_RESPONSE_SUBSTRING" ]]; then
  grep -Fq "$EXPECTED_RESPONSE_SUBSTRING" "$work_dir/response.json" || {
    echo "response did not contain EXPECTED_RESPONSE_SUBSTRING" >&2
    exit 1
  }
fi

deadline=$((SECONDS + ACCEPTANCE_TIMEOUT_SECONDS))
matched_line=""
while ((SECONDS < deadline)); do
  kafka_offsets >"$work_dir/offsets-after"
  consume_new_records "$work_dir/offsets-before" "$work_dir/offsets-after" "$work_dir/new-records"
  matched_line=$(grep -F "$marker" "$work_dir/new-records" | head -n 1 || true)
  [[ -n "$matched_line" ]] && break
  sleep 1
done
[[ -n "$matched_line" ]] || { echo "no Kafka event matched marker $marker" >&2; exit 1; }

message_key=${matched_line%%|*}
event_json=${matched_line#*|}
printf '%s\n' "$event_json" >"$work_dir/event.json"

response_request_id=$(awk -F': *' 'tolower($1) == "x-request-id" {gsub("\r", "", $2); print $2}' "$work_dir/response.headers" | tail -n1)
response_run_id=$(awk -F': *' 'tolower($1) == "x-ai-gateway-run-id" {gsub("\r", "", $2); print $2}' "$work_dir/response.headers" | tail -n1)

jq -e \
  --arg marker "$marker" \
  --arg key "$message_key" \
  --arg response_request_id "$response_request_id" \
  --arg response_run_id "$response_run_id" \
  --arg api_key_id "$SAFE_API_KEY_ID" \
  --argjson expect_s3_body_object "$EXPECT_S3_BODY_OBJECT" \
  --argjson reserved_input_tokens "$SAFE_RESERVED_INPUT_TOKENS" '
    .schema_version == 1 and
    .event_type == "request_completed" and
    .operation == "chat" and
    .protocol == "openai.chat_completions" and
    .transport == "http" and
    .client.name == "curl" and
    .success == true and
    .request_id == $key and
    .request_id == $response_request_id and
    .run_id == $response_run_id and
    (.event_id | length > 0) and
    (.run_id | length > 0) and
    (.correlation.provider_response_id | length > 0) and
    .billing.api_key_id == $api_key_id and
    .billing.reserved_input_tokens == $reserved_input_tokens and
    .capture_policy.mode == "bounded_inline" and
    .capture_policy.max_inline_bytes > 0 and
    (if $expect_s3_body_object then
      .capture_policy.external_body_store.provider == "s3" and
      .request_body.truncated == true and
      .request_body.object.provider == "s3" and
      (.request_body.object.bucket | length > 0) and
      (.request_body.object.key | length > 0) and
      (.request_body | has("external_storage_error") | not)
    else
      .request_body.truncated == false
    end) and
    (.headers | has("authorization") | not) and
    (.request_body.content | contains($marker)) and
    .upstream_request_body.same_as == "request_body" and
    .response_body.truncated == false
  ' "$work_dir/event.json" >/dev/null

jq -c '{
  acceptance: "PASS",
  event_id,
  run_id,
  request_id,
  conversation_key,
  provider_response_id: .correlation.provider_response_id,
  billing,
  capture_policy,
  tokens,
  request_body: {sha256: .request_body.sha256, size_bytes: .request_body.size_bytes, object: .request_body.object},
  response_body: {sha256: .response_body.sha256, size_bytes: .response_body.size_bytes}
}' "$work_dir/event.json"
