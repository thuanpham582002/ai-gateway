# AI Platform Roadmap - Brainstorm

## Current State (As of 2026-03-04)

### Implemented Features (~70%)

| Category | Feature | Status |
|----------|---------|--------|
| **Token Metrics** | `gen_ai.client.token.usage` (input/output) | ✅ Done |
| **Latency** | `gen_ai.server.request.duration` | ✅ Done |
| **TTFT** | `gen_ai.server.time_to_first_token` | ✅ Done |
| **Per-token Latency** | `gen_ai.server.time_per_output_token` | ✅ Done |
| **Request Count** | `ai_gateway_calls_total` (via spanmetrics) | ✅ Done |
| **User Tracking** | `session_id` label on metrics | ✅ Done |
| **API Key Tracking** | `api_key` label on metrics | ✅ Done |
| **Model Tracking** | `gen_ai.request.model`, `gen_ai.response.model` | ✅ Done |
| **Provider Tracking** | `gen_ai.provider.name` | ✅ Done |
| **Distributed Tracing** | Jaeger integration via OTEL | ✅ Done |
| **Prometheus Export** | Native metrics on extproc:1064 | ✅ Done |

### Infrastructure Setup

```
AI Gateway ExtProc (port 1064)
    ↓ OTLP/HTTP
OTEL Collector (observability namespace)
    ├── → Jaeger (traces)
    └── → Prometheus scrape (metrics)
```

**Configuration:**
- Controller flags: `--spanRequestHeaderAttributes`, `--metricsRequestHeaderAttributes`
- GatewayConfig with OTEL env vars
- Header mapping: `X-Session-ID` → `session.id`, `X-API-Key` → `api.key`

---

## Missing for Production-Grade (~30%)

### 1. Quality/Safety Metrics (High Priority)

Measure LLM output quality, not just performance.

| Metric | Description | Implementation Approach |
|--------|-------------|------------------------|
| **Hallucination Rate** | % responses with false info | LLM-as-judge, fact-checking |
| **Toxicity Score** | Harmful content detection (0-1) | Perspective API, Detoxify |
| **Factual Correctness** | % responses matching ground truth | RAG comparison |
| **Relevance Score** | Response relevance to question | Semantic similarity |
| **PII Detection** | Personal info leak detection | Regex + NER models |
| **Prompt Injection** | Attack detection | Pattern matching, classifier |

**Tools to evaluate:**
- Guardrails AI (open source)
- NeMo Guardrails (NVIDIA)
- Lakera Guard (commercial)
- LLM Guard (open source)

**Integration points:**
- ExtProc response processing
- Post-response webhook
- Async evaluation pipeline

---

### 2. Advanced Cost Management (High Priority)

Beyond token counting - actual budget management.

| Feature | Description | Implementation |
|---------|-------------|----------------|
| **USD Cost Tracking** | Real cost calculation | Token × model price lookup |
| **Budget Alerts** | Quota warnings | Prometheus alerting rules |
| **Cost Forecasting** | Predict future spend | Time-series analysis |
| **Rate Limiting by Cost** | Limit by $ not requests | Envoy rate limit + metadata |
| **Cost Allocation** | Per-team/project breakdown | Label-based aggregation |
| **Model Cost Comparison** | Compare model efficiency | PromQL queries |

**Price table example:**
```yaml
model_prices:
  gpt-4:
    input: 0.03    # per 1K tokens
    output: 0.06
  gpt-3.5-turbo:
    input: 0.0005
    output: 0.0015
  claude-3-opus:
    input: 0.015
    output: 0.075
```

**PromQL for cost:**
```promql
# Estimated cost per user
sum by (session_id) (
  gen_ai_client_token_usage_sum{gen_ai_token_type="input"} * 0.00003 +
  gen_ai_client_token_usage_sum{gen_ai_token_type="output"} * 0.00006
)
```

---

### 3. Evaluation Pipelines (Medium Priority)

Automated LLM quality testing.

```
┌─────────────────────────────────────────────────────────────┐
│                    Evaluation Pipeline                       │
├─────────────────────────────────────────────────────────────┤
│  Test Dataset    →    LLM Response    →    Evaluators       │
│  (Q + expected)       (actual)             (scoring)         │
├─────────────────────────────────────────────────────────────┤
│  Output: Accuracy, Hallucination%, Latency P95, Cost/answer │
└─────────────────────────────────────────────────────────────┘
```

| Component | Description | Tools |
|-----------|-------------|-------|
| **Test Datasets** | Questions + ground truth | JSON/YAML files |
| **Automated Evaluators** | Scoring logic | LLM-as-judge, regex, embeddings |
| **Regression Testing** | Compare model versions | CI/CD integration |
| **A/B Testing** | Compare prompts/models | Traffic splitting + metrics |
| **Continuous Eval** | Scheduled tests | CronJob + reporting |

**Tools to evaluate:**
- Langfuse Evals
- Arize Phoenix (open source)
- Braintrust
- Ragas (RAG-specific)
- DeepEval

---

## Implementation Priority

### Phase 1: Cost Management (1-2 weeks)
1. Add model price configuration to AIGatewayRoute or ConfigMap
2. Calculate USD cost in extproc, add to metrics
3. Create Grafana dashboard with cost breakdown
4. Set up Prometheus alerts for budget thresholds

### Phase 2: Basic Guardrails (2-3 weeks)
1. Integrate LLM Guard or similar for PII/toxicity
2. Add guardrail metrics to OTEL export
3. Optional: Block requests that fail guardrails

### Phase 3: Evaluation Pipeline (3-4 weeks)
1. Define test dataset format
2. Build evaluation runner (CronJob)
3. Integrate with existing metrics/tracing
4. Create evaluation dashboard

---

## Reference Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Production AI Platform                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  ┌─────────┐    ┌─────────────┐    ┌─────────────┐               │
│  │ Client  │───▶│ AI Gateway  │───▶│ LLM Backend │               │
│  └─────────┘    │ (Envoy+ExtProc)  └─────────────┘               │
│                 └──────┬──────┘                                   │
│                        │                                          │
│         ┌──────────────┼──────────────┐                          │
│         ▼              ▼              ▼                          │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐                    │
│  │ Guardrails │ │  Metrics   │ │  Tracing   │                    │
│  │ (Quality)  │ │(Prometheus)│ │  (Jaeger)  │                    │
│  └────────────┘ └────────────┘ └────────────┘                    │
│         │              │              │                          │
│         └──────────────┼──────────────┘                          │
│                        ▼                                          │
│              ┌─────────────────┐                                  │
│              │    Grafana      │                                  │
│              │   Dashboard     │                                  │
│              └─────────────────┘                                  │
│                        │                                          │
│         ┌──────────────┼──────────────┐                          │
│         ▼              ▼              ▼                          │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐                    │
│  │   Alerts   │ │   Billing  │ │ Eval Jobs  │                    │
│  │(PagerDuty) │ │  Reports   │ │  (CronJob) │                    │
│  └────────────┘ └────────────┘ └────────────┘                    │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

---

## Industry Benchmarks

Based on web research (2026-03):

| Platform | Strengths | We Should Learn |
|----------|-----------|-----------------|
| **Langfuse** | Session tracking, prompt management | Prompt versioning |
| **Arize** | ML observability, drift detection | Anomaly detection |
| **Datadog LLM** | Full-stack integration | Unified dashboards |
| **Helicone** | Cost tracking, caching | Request caching |
| **Galileo** | Quality evaluation | LLM-as-judge |

**OpenTelemetry GenAI Semantic Conventions (v1.37+):**
- We comply with standard metric names ✅
- Standard attributes for model/provider ✅
- Token usage histograms ✅

---

## Open Questions

1. **Guardrails placement:** Pre-request, post-response, or both?
2. **Cost calculation:** Real-time in extproc or async aggregation?
3. **Evaluation frequency:** Per-deployment or continuous?
4. **Multi-tenancy:** How to isolate metrics per tenant?
5. **Data retention:** How long to keep traces/metrics?

---

## Related Docs

- [OTEL Metrics Setup](./otel-metrics-setup.md) - Current implementation
- [CLAUDE.md](../CLAUDE.md) - Project overview
