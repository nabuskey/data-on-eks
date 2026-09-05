# StarRocks High-Throughput Ingest Runbook

Kafka -> StarRocks **Routine Load** -> StarRocks tables, on EKS with local-NVMe
Graviton nodes in a **single AZ**. This guide covers **500k rows/s** and
**1,000,000 rows/s** of large JSON records (~57 KB each), how to set it up, how to
run it, and how to run it **sustained** (steady-state for hours).

Record size matters: at ~57 KB/record, 500k/s ≈ **28.5 GB/s** and 1M/s ≈ **57 GB/s**
of raw data. This is a **bandwidth and storage** problem far more than a CPU problem
— StarRocks BE CPU stayed under ~65% even at 1.1M rows/s.

---

## Load generator & manifests

Manifests are under `manifests/kafka/` and `manifests/starrocks/`.

**The load generator (producer).** `manifests/kafka/events-producer-deployment.yaml`
runs a Go producer that builds realistic JSON records and writes them to the
`events` topic. The record shape comes from the `bench-schema` ConfigMap
(`sample.json`); the bulk is a compressible `_filler` field. Tune via container args:
`-min-size`/`-max-size` (record bytes, ~10–100 KB here → ~57 KB avg),
`-compressibility` (~4:1), `-goroutines`, `-cpus`, `-acks`, `-compression`. Scale the
**rate** with the Deployment `replicas`.

---

## 0. Design decisions (and why)

- **Routine Load, not Kafka Connect.** Routine Load streams the JSON parse inside
  the BEs. The Kafka connector's JSON path re-serializes on the workers and parses
  each batch whole-in-memory, capping around ~200k rows/s for large records.
- **Single AZ.** Avoids cross-AZ data-transfer cost and eliminates rack-aware
  partition skew (a lone broker in one AZ gets ~half the partitions under RF1).
- **Local NVMe (r8gd) for both Kafka and StarRocks BE storage.** Removes the EBS
  per-volume (1000 MB/s) and per-instance bandwidth ceilings that throttle EBS-backed
  nodes.
- **Shard writes across many tables at high rates.** A single StarRocks table's
  load-transaction (publish) path saturates around **~900k rows/s** with hundreds of
  concurrent load tasks (~50% of task transactions abort). Sharding across N tables
  removes this contention.

---

## 1. Resource requirements

| Tier | 500k rows/s | 1,000,000 rows/s |
|---|---|---|
| **Kafka brokers** | ~12 × r8gd.12xlarge (NVMe) | ~21 × r8gd.12xlarge (NVMe) |
| **Kafka controllers** | 3 × r8g.4xlarge (KRaft) | 3 × r8g.4xlarge |
| **StarRocks FE** | 1–3 × r8g.4xlarge | 3 × r8g.4xlarge |
| **StarRocks BE** | ~12 × r8gd.4xlarge (NVMe) | ~24 × r8gd.4xlarge (NVMe) |
| **Producer (test)** | ~48 pods (4 cpu, 128 goroutines) | ~100 pods |
| **Topic partitions** | 384 | 768 |
| **Routine Load tasks** | ~384 (16–32 jobs × up to aliveBE) | 768 (32 jobs × 24) |
| **Target table(s)** | 1 table OK (48–120 buckets) | **shard: ~32 tables** (24–48 buckets each) |
| **Peak BE CPU seen** | ~60% (12 BE) | ~61% (24 BE) |

Sizing rationale:
- **Brokers:** each r8gd.12xlarge sustains ~2.6–2.8 GB/s network. 21 × 2.7 ≈ 57 GB/s.
- **BEs:** BEs are the working tier under Routine Load; ~24 BEs run ~60% at 1M, so
  headroom remains.
- **Producer:** per pod ~11–15k rows/s (parallelism-bound, not CPU-bound); scale
  replicas to reach ~1.1× the target so the ingester, not the producer, is the limit.

All nodes are pinned to a **single AZ** and to a nodepool that RAID-mounts local NVMe
(here: `memory-optimized-graviton`), via `karpenter.sh/nodepool` +
`topology.kubernetes.io/zone` in the pod affinity/nodeSelector.

---

## 2. Cluster setup

Manifests live under `manifests/`. Key configuration:

### Kafka (Strimzi, KRaft)
- Broker `KafkaNodePool`: `r8gd.12xlarge`, `on-demand`, single AZ, pinned to the
  NVMe nodepool, **one broker per node** (pod anti-affinity), storage `type:
  ephemeral` (lands on the RAID0 NVMe) — or a **local-PV** class for sustained runs
  (see §4).
- Controllers: separate `KafkaNodePool`, `r8g.4xlarge`.
- Broker tuning: `num.io.threads=16`, `num.network.threads=8`,
  `num.replica.fetchers=8`.

### StarRocks (operator)
- FE and BE pinned to single AZ + NVMe nodepool; **one BE per node** (anti-affinity).
- **Persisted config via ConfigMaps** (`manifests/starrocks/conf/*.conf`, referenced
  by `configMapInfo` in the cluster CR):
  - BE `be.conf`: `streaming_load_max_batch_size_mb = 1024` (raise the 100 MB JSON
    Stream Load cap so large-record loads pass without `ignore_json_size`).
  - FE `fe.conf`: `max_routine_load_task_concurrent_num = 24`,
    `max_routine_load_task_num_per_be = 32`.

### Topic
```
partitions: 768   # 384 for 500k
replication.factor: 1   # RF>=2 for sustained/production (see §4)
```
After creating the topic, verify it is healthy and even:
```
kafka-topics.sh --describe --topic events --unavailable-partitions   # expect none
# leader count should equal broker count; ~equal partitions per broker
```

### Tables
Schema (per table):
```sql
CREATE TABLE events (
  ingest_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  id BIGINT NOT NULL, name VARCHAR(256), active BOOLEAN, score DOUBLE,
  tags JSON, meta JSON, note STRING NULL, filler STRING
)
DUPLICATE KEY(ingest_time, id)
PARTITION BY date_trunc('hour', ingest_time)
DISTRIBUTED BY HASH(id) BUCKETS 24
PROPERTIES ('replication_num' = '1');   -- 2 for sustained/production
```
- **500k:** one table is fine.
- **1M:** create **~32 shard tables** `events_0 .. events_31`, one per Routine Load
  job, so each table has only ~24 concurrent committers instead of ~768.

### Routine Load jobs
One job per (partition-slice, table). For 1M: 32 jobs, each 24 partitions -> its own
shard table:
```sql
CREATE ROUTINE LOAD bench.rlt_<j> ON events_<j>
COLUMNS(id, name, active, score, tags, meta, note, filler)
PROPERTIES (
  "format" = "json",
  "jsonpaths" = "[\"$.id\",\"$.name\",\"$.active\",\"$.score\",\"$.tags\",\"$.meta\",\"$.note\",\"$._filler\"]",
  "desired_concurrent_number" = "24",
  "max_batch_interval" = "10"
)
FROM KAFKA (
  "kafka_broker_list" = "data-on-eks-kafka-bootstrap.kafka.svc:9092",
  "kafka_topic" = "events",
  "kafka_partitions" = "<24 partition ids>",
  "property.kafka_default_offsets" = "OFFSET_END"
);
```
Task math: per job tasks = min(partitions, desired, aliveBE, max_concurrent). With
aliveBE=24 and 24 partitions/job, each job = 24 tasks; 32 jobs = 768 tasks (32/BE).
Do **not** raise `max_batch_interval` to reduce load — larger, longer-held
transactions *increase* commit contention.

**Generator (1M: 32 shard tables + 32 jobs, 24 partitions each).** Copy-paste:
```bash
FE="kubectl -n starrocks exec -i sr-bench-fe-0 -- mysql -h127.0.0.1 -P9030 -uroot"
JP='[\"$.id\",\"$.name\",\"$.active\",\"$.score\",\"$.tags\",\"$.meta\",\"$.note\",\"$._filler\"]'
for j in $(seq 0 31); do
  # shard table
  echo "CREATE TABLE bench.events_$j (ingest_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    id BIGINT NOT NULL, name VARCHAR(256), active BOOLEAN, score DOUBLE, tags JSON, meta JSON,
    note STRING NULL, filler STRING) DUPLICATE KEY(ingest_time, id)
    PARTITION BY date_trunc('hour', ingest_time) DISTRIBUTED BY HASH(id) BUCKETS 24
    PROPERTIES ('replication_num'='1');" | $FE   # replication_num=2 for sustained
  # routine load job -> its own table, its own 24-partition slice
  base=$((j*24)); parts=$(seq $base $((base+23)) | paste -sd,)
  echo "CREATE ROUTINE LOAD bench.rlt_$j ON events_$j
    COLUMNS(id, name, active, score, tags, meta, note, filler)
    PROPERTIES ('format'='json','jsonpaths'='$JP','desired_concurrent_number'='24','max_batch_interval'='10')
    FROM KAFKA ('kafka_broker_list'='data-on-eks-kafka-bootstrap.kafka.svc:9092',
    'kafka_topic'='events','kafka_partitions'='$parts','property.kafka_default_offsets'='OFFSET_END');" | $FE
done
```
For **500k**: one table `events`, ~16 jobs over the 384 partitions (24 partitions
each), `ON events` for all jobs.

---

## 3. How to run

`FE` below is a convenience alias for running SQL against the StarRocks FE.

```
FE="kubectl -n starrocks exec -i sr-bench-fe-0 -- mysql -h127.0.0.1 -P9030 -uroot"

# 1. Node pools + Kafka
kubectl apply -f manifests/kafka/node-pool-controller.yaml
kubectl apply -f manifests/kafka/node-pool-broker.yaml
kubectl apply -f manifests/kafka/kafka-cluster.yaml
# wait: all brokers 1/1 AND registered; confirm all on 2.6T NVMe (df /var/lib/kafka)

# 2. Topic (verify 0 unavailable partitions, even leader spread)
kubectl apply -f manifests/kafka/events-topic.yaml

# 3. StarRocks (wait FE + all BE Alive:true; confirm BE storage on NVMe)
kubectl apply -f manifests/starrocks/starrocks-cluster.yaml

# 4. Tables + Routine Load jobs  (see the generator snippet in section 2)

# 5. Smoke test FIRST: producer at a few pods, confirm rows land and jobs stay RUNNING
kubectl -n kafka scale deploy events-producer --replicas=4

# 6. Then scale the producer to feed ~1.1x the target
kubectl -n kafka scale deploy events-producer --replicas=<48|100>

# 7. Measure
#   ingest  = delta of SUM(count(*)) across target table(s) over a 60-120s window
#   produce = delta of SUM(end offsets) of the topic
#   BE CPU  = kubectl -n starrocks top pod -l app.kubernetes.io/component=be
#   RL health = SHOW ROUTINE LOAD  (committedTaskNum vs abortedTaskNum; State RUNNING)
```

Always **smoke-test at a low producer rate first** and confirm rows land + jobs stay
`RUNNING` before scaling up — it catches topic/table/config mistakes cheaply before
provisioning dozens of NVMe nodes.

Healthy signs: all jobs `State: RUNNING`, `abortedTaskNum` ≈ 0 (a high abort rate
with `errorRows=0` means single-table transaction contention — shard more), and
ingest tracking produce.

Expected results (validated): **~526k rows/s** (12 BE) and **~1.1M rows/s** (24 BE,
32 shard tables), BEs ~60%.

---

## 4. Running sustained (steady-state)

A short burst only needs throughput. A sustained run needs **durability** and
**bounded storage**, because at these rates local disks fill in minutes and any
single node loss under RF1 stops ingestion.

### 4.1 Replication factor >= 2 (required)
- Kafka topic `replication.factor >= 2` and StarRocks table `replication_num = 2`.
- Why: at RF1 a single broker or BE eviction makes its partitions/tablets
  unavailable ("Tablet lost replicas") and Routine Load pauses cluster-wide. RF2
  lets a replica take over with no interruption. RF2 roughly **doubles** broker and
  BE write + storage.

### 4.2 BE storage: local **PersistentVolumes**, not hostPath/emptyDir (required)
- Back BE storage with a **local-volume PV** (local static provisioner on the
  `ephemeral-nvme-local-provisioner` nodepool) or a dedicated data volume — **not**
  `hostPath`/`emptyDir`.
- Why: `hostPath`/`emptyDir` data counts against the node's kubelet
  **ephemeral-storage**. As tablet data grows it trips **DiskPressure**, kubelet
  **evicts the BE**, and the node gets a `disk-pressure` taint so the BE cannot
  reschedule. A dedicated local PV is not counted as ephemeral storage, so data
  growth does not cause eviction.

### 4.3 Bound local disk with tiering to Iceberg (required for sustained runs)
Do the math (1M/s, ~12 KB/row on disk after columnar compression):
- ~12 GB/s to BE storage ≈ **~43 TB/hour (RF1), ~86 TB/hour (RF2)**.
- Across 24 BEs that is ~1.8 TB/BE/hour (RF1) — an r8gd.4xlarge has only ~950 GB
  NVMe, so local disk is exhausted in well under an hour.

Therefore you cannot keep more than a short window of data on local BE disk during
a sustained run. Options:
- **Tier continuously to Iceberg/S3 (recommended).** Keep only a small *hot window*
  (e.g. 10–15 min) in the StarRocks native table; a scheduled job moves aged rows to
  an Iceberg (Glue/S3) cold table and deletes them from hot. Local disk then holds
  ~450 GB/BE (fits 950 GB). This is the lakehouse pattern (`iceberg_glue` catalog +
  `events_cold` + tiering CronJob).
- Or scale BE local capacity to hold the full run (bigger r8gd sizes / many more
  BEs) — usually not economical for large records.

### 4.4 Bound Kafka broker disk with retention
- Set topic `retention.ms` / `retention.bytes` to a **bounded** window (e.g. 10–15
  min), sized larger than the worst-case consumer lag.
- Do **not** use unlimited retention (`retention.ms=-1`) at these rates — broker NVMe
  fills and brokers get evicted.
- Broker disk needed ≈ target × 57 KB × retention_seconds × RF / broker_count.
  Example: 1M/s × 57 KB × 900 s × RF2 / 21 ≈ ~4.9 TB/broker — an r8gd.12xlarge (~3.4
  TB NVMe) is short for RF2 at 15 min; use a larger r8gd size (more NVMe), more
  brokers, or a shorter window.

### 4.5 Keep compaction ahead of ingest
- Shard across tables and use enough buckets (~3–4 tablets/BE per shard) so no single
  tablet accumulates versions faster than compaction clears them. If loads start
  failing with version/compaction pressure, add buckets/shards or BEs.

### 4.6 Sustained-run checklist
- [ ] Kafka topic RF>=2, bounded retention sized to lag window.
- [ ] StarRocks table(s) `replication_num=2`, sharded (~32 for 1M).
- [ ] BE storage on local PV (or dedicated volume), NOT hostPath/emptyDir.
- [ ] Tiering-to-Iceberg job running; hot table stays bounded.
- [ ] All nodes single-AZ, on the NVMe nodepool, one broker/BE per node.
- [ ] Persisted FE/BE configs (ConfigMaps), not runtime-only.
- [ ] Producer feeds ~1.1× target (ingester is the limiter, not the producer).

### 4.7 Monitor during the run
- Routine Load: `SHOW ROUTINE LOAD` — `State` stays RUNNING, `abortedTaskNum` low,
  consumer lag bounded (not growing).
- BE: CPU, and **local disk %** (must stay flat, proving tiering keeps up).
- Kafka: broker disk % (flat, proving retention keeps up), no under-replicated or
  offline partitions.
- StarRocks: tablet version counts / compaction score (not climbing).

### 4.8 Failure modes and fixes
| Symptom | Cause | Fix |
|---|---|---|
| RL jobs pause, ~50% task aborts, `errorRows=0` | single-table load-txn contention | shard across more tables |
| BE evicted, "Tablet lost replicas", RL pauses | BE disk full (hostPath = kubelet ephemeral) | local-PV storage + tiering + RF>=2 |
| Brokers evicted / partitions leaderless | broker disk full (unbounded/large retention) | bounded retention; bigger/more brokers; RF>=2 |
| "exceed max size 100MB of json type data" | default JSON Stream Load cap | BE `streaming_load_max_batch_size_mb` (already 1024) |
| Partitions skewed to one broker | multi-AZ + rack-aware assignment | single AZ (all brokers one rack) |
| Nth broker/BE stuck Pending | single instance-type + AZ + per-node anti-affinity wedging provisioning | allow several NVMe instance sizes; ensure AZ capacity; clean rebuild |

---

## 5. Kafka Connect: how we measured it and what we found

We benchmarked the **StarRocks Kafka Connect sink** as the alternative ingester
before choosing Routine Load.

**How measured.** Deployed a Strimzi `KafkaConnect` cluster (workers with 8 GB heap)
plus the StarRocks sink `KafkaConnector` (`strip_outer_array=true`, JSON converter,
`tasksMax` up to the partition count), consuming the same `events` topic and loading
the same table. We fed it hot from the same producer and measured: committed rows/s
(row-count delta), Connect **worker CPU**, and **BE CPU**, plus the connector task
states and worker logs.

**What we found (large ~57 KB JSON records):**
- **Throughput capped ~150–200k rows/s** — well below Routine Load's ~1M — and
  adding workers barely helped.
- **Worker-CPU bound, BEs idle.** The connector **deserializes every record and
  re-serializes** it to JSON for Stream Load on the *workers*; workers ran ~80–90%
  CPU while BEs stayed **under ~20%**. Compute was spent on the connect tier, not on
  StarRocks.
- **Whole-batch JSON + the 100 MB cap.** StarRocks parses a JSON Stream Load body
  whole-in-memory, and the connector's Stream Load SDK emits ~128 MB chunks, which
  exceed the default 100 MB JSON cap. You must either raise
  `streaming_load_max_batch_size_mb` or set `ignore_json_size=true` (a memory
  hazard). Making batches *larger* made throughput *worse* (bigger in-memory parse).
- **No real backpressure.** `bufferflush.maxbytes` is only a flush *trigger*, not a
  consumption limit; when the load path lagged, per-task buffers grew to **multiple
  GB** and risked worker OOM. A short `bufferflush.intervalms` (~1 s) bounded the
  buffer but cost throughput.
- **Single-FE Stream Load funnel.** All tasks POST Stream Loads through the FE HTTP
  endpoint; at high task counts this became a connection chokepoint.

**Measurement caveat (important).** Our first Connect-vs-Routine-Load comparison was
**confounded by a storage bottleneck**: Connect was draining a *cold backlog* off
EBS-backed brokers (disk-I/O-bound), which understated it. Re-run cleanly on the
NVMe brokers with a *hot* feed, Connect improved but still plateaued ~200k because of
the JSON re-serialize + whole-batch-parse + buffer design above.

**Conclusion.** For **large JSON**, Routine Load is dramatically better (streams the
parse on the BEs; no worker re-serialize; no 100 MB JSON cap; scales with
partitions/tasks and table sharding). **Kafka Connect is the right choice for CSV**
(the StringConverter passthrough streams, no JSON size cap, no re-serialization) or
when Connect-only features are required — CDC (Debezium), Protobuf/Avro via
Schema-Registry converters, multi-topic/multi-sink fan-out, or SMTs.

---

## 6. Teardown
Delete the workloads so the (expensive) NVMe nodes deprovision:
```
kubectl -n kafka delete deploy events-producer
kubectl -n kafka delete kafka data-on-eks; kubectl -n kafka delete kafkanodepool --all
kubectl -n kafka delete kafkatopic --all; kubectl -n kafka delete pvc --all
kubectl -n starrocks delete starrockscluster --all; kubectl -n starrocks delete pvc --all
# empty nodes reclaim on Karpenter consolidation; to expedite, delete the NodeClaims
kubectl delete nodeclaim <r8gd nodeclaims>   # deleting Nodes alone does NOT terminate instances
```
