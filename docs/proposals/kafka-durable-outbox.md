# Kafka Durable Outbox

## Status

Implemented for the native Kafka event publisher.

## Problem

The previous publisher connected to Kafka during ExtProc startup and sent each event directly to a Sarama asynchronous producer. Its optional JSON spool replayed only at startup and the HLA deployment mounted it inside the UDS `emptyDir`. Kafka unavailability could therefore delay startup or publishing, and pod replacement discarded queued audit events.

## Design

The request path serializes the audit event and commits it to a local bbolt outbox. A single background shipper reads ready records in sequence order, creates a synchronous idempotent Kafka producer lazily, sends one record, and deletes it only after broker acknowledgement.

The transaction record contains the stable `event_id`, Kafka key, JSON value, readiness state, enqueue time, and a SHA-256 checksum. Startup validates records, quarantines corrupt entries, promotes interrupted S3-upload snapshots, and imports the old per-event JSON spool format.

The outbox has independent byte and record limits. Inference remains fail-open when storage is full or unavailable; loss is observable through counters, gauges, and logs. This policy prevents an audit outage from becoming an inference outage.

## Guarantees

- Kafka is absent from the request path and from startup.
- Committed events survive process and pod restart when the directory is persistent.
- Delivery is at least once; consumers deduplicate by `event_id`.
- Ready events are sent in local sequence order.
- A corrupt record does not permanently block later records.
- Capacity is bounded and overflow is explicitly observable.

## HLA Placement

HLA has no replicated RWX storage class for this workload. The deployment therefore uses one Envoy pod per control-plane node and a retained hostPath outbox on each node. Rolling updates use `maxSurge: 0` and `maxUnavailable: 1` so two processes never contend for the same bbolt file.

This covers Kafka outages, container restarts, and same-node pod replacement. Permanent node or disk loss remains a documented residual risk. Moving to replicated block storage or an external outbox is required to remove that risk.
