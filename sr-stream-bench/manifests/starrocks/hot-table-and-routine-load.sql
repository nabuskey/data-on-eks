CREATE DATABASE IF NOT EXISTS bench;

-- Hot table. Hourly partitions (date_trunc supports hour/day/month/year).
-- The 5-minute tiering validation moves rows by predicate
-- (WHERE ingest_time < now() - 5 min), independent of partition size.
-- Production would use date_trunc('day', ingest_time) + DROP PARTITION.
CREATE TABLE IF NOT EXISTS bench.events (
  ingest_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  id          BIGINT   NOT NULL,
  name        VARCHAR(256),
  active      BOOLEAN,
  score       DOUBLE,
  tags        JSON,
  meta        JSON,
  note        STRING NULL,
  filler      STRING
)
DUPLICATE KEY(ingest_time, id)
PARTITION BY date_trunc('hour', ingest_time)
DISTRIBUTED BY HASH(id) BUCKETS 12
PROPERTIES ("replication_num" = "1");

-- Routine Load: consume JSON from Kafka into the hot table.
-- ingest_time is not mapped; it takes the CURRENT_TIMESTAMP default at load.
-- max_batch_interval=10s keeps freshness well within 30-60s.
CREATE ROUTINE LOAD bench.rl_events ON events
COLUMNS(id, name, active, score, tags, meta, note, filler)
PROPERTIES (
  "format" = "json",
  "jsonpaths" = "[\"$.id\",\"$.name\",\"$.active\",\"$.score\",\"$.tags\",\"$.meta\",\"$.note\",\"$._filler\"]",
  "desired_concurrent_number" = "3",
  "max_batch_interval" = "10"
)
FROM KAFKA (
  "kafka_broker_list" = "data-on-eks-kafka-bootstrap.kafka.svc:9092",
  "kafka_topic" = "events",
  "property.kafka_default_offsets" = "OFFSET_END"
);
