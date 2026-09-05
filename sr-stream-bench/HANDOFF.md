# Kafka Throughput Benchmark — Handoff

## Goal
Benchmark Kafka on EKS for a production workload: 1 million records/s of large
records (10–100 KB each). A Go producer generates data from a sample JSON
schema and writes it to Strimzi Kafka topics. Data content does not matter.

## Status: goal met
Achieved **1,080,106 records/s** (~61 GB/s logical) for 120s, 0 under-replicated.
Config: 24 brokers, 384-partition RF1 topic, acks=1, lz4, compressibility=3,
100 producer pods (4 cores / 128 goroutines each).

## Repository layout
- `main.go`, `generator.go` — the Go producer. Reads a sample record, builds a
  generation plan, pads records to a random size with a `_filler` field.
- `generator_test.go` — unit tests (`go test ./...`).
- `sample.json` — example sample record.
- `Dockerfile` — multi-stage, static, distroless. Base images pinned by digest.
- `scripts/run-bench.sh` — runs one benchmark: creates the topic, runs N pods,
  prints the aggregate rate, and ALWAYS deletes the topic on exit.
- `manifests/kafka/` — Strimzi cluster, node pools, storage class, topic,
  schema ConfigMap, producer Job.
- `kubeconfig.yaml` — EKS access (cluster `sr-ch-bench`, region us-west-2).

## Using the CLI (the Go producer directly)
Build and run locally (needs a reachable broker, e.g. a local Docker Kafka;
the EKS internal listener is not reachable from a laptop):
```
go build -o kafka-bench .
./kafka-bench \
  -topic-file=bench,sample.json \
  -kafka-url=localhost:9092 \
  -cpus=1 -goroutines=64 \
  -min-size=10240 -max-size=102400 \
  -reuse=true -compressibility=3 \
  -acks=1 -compression=lz4 -duration=30s
```
On exit it prints one line: `messages=N bytes=N elapsed=...`. Measure the rate
externally (Prometheus / Kafka Exporter).

Full flag reference:
- `-cpus` int: usable CPUs (sets GOMAXPROCS). 0 = all. Default 0.
- `-goroutines` int: producer goroutines per topic. Default 10.
- `-topic-file` topic,path: repeatable. Maps one topic to one sample record.
- `-kafka-url` host:port: broker bootstrap address. Required.
- `-min-size` / `-max-size` bytes: record size range. Default 10240 / 102400.
- `-reuse` bool: reuse one record per goroutine (fast). Default true.
- `-compressibility` float: filler lz4 ratio target. 1 = random. Default 1.
- `-duration` dur: e.g. 60s. 0 = run until stopped (pod use). Default 0.
- `-acks` 0|1|-1: producer acks. Default 1.
- `-compression` none|gzip|snappy|lz4|zstd. Default lz4.
- `-batch-bytes` / `-batch-size` / `-linger`: producer batching. Defaults
  1048576 / 100 / 10ms. Keep defaults; large batches hurt.
Run `./kafka-bench -h` for the live list.

Run the CLI as a Kubernetes Job (in-cluster) instead of the script:
```
export KUBECONFIG=./kubeconfig.yaml
kubectl apply -f manifests/kafka/bench-schema-configmap.yaml   # schema mount
# create a KafkaTopic first (see manifests/kafka/bench-topic.yaml), then:
kubectl apply -f manifests/kafka/producer-job.yaml
kubectl -n kafka logs -f job/kafka-bench-producer               # see exit line
```

## Using scripts/run-bench.sh (recommended for cluster tests)
It creates the topic, launches the producer pods, prints the aggregate rate,
and always deletes the topic on exit (even on error or Ctrl-C).
```
export KUBECONFIG=./kubeconfig.yaml

# small smoke test
PODS=2 PARTITIONS=48 RF=3 DURATION=30s ./scripts/run-bench.sh

# per-broker ceiling probe (RF1, no replication)
PODS=24 PARTITIONS=96 RF=1 ACKS=1 COMPRESSIBILITY=3 DURATION=60s \
  ./scripts/run-bench.sh

# full 1M/s run (large, costly — provision brokers first)
TOPIC=bench1m PARTITIONS=384 RF=1 PODS=100 GOROUTINES=128 CPUS=4 \
  ACKS=1 COMPRESSION=lz4 COMPRESSIBILITY=3 REUSE=false DURATION=120s \
  ./scripts/run-bench.sh
```
Env vars (defaults in brackets): TOPIC[bench] PARTITIONS[48] RF[3] PODS[2]
GOROUTINES[128] CPUS[4] ACKS[-1] COMPRESSION[lz4] COMPRESSIBILITY[3]
REUSE[false] DURATION[60s] MINSIZE[10240] MAXSIZE[102400]
BATCH_BYTES[1048576] BATCH_SIZE[100] LINGER[10ms].
Output line: `RESULT ...` with rec/s and MB/s, then the under-replicated count.

To scale brokers before a big run, edit `replicas` in
`manifests/kafka/node-pool-broker.yaml` and `kubectl apply` it; wait for
`kubectl -n kafka wait kafka/data-on-eks --for=condition=Ready`.

## Producer flags (set as Job args or via run-bench.sh env)
See the "Using the CLI" section above for the full flag reference.
- `-reuse=true` (default): build one record per goroutine, resend it. Fast.
- `-reuse=false`: regenerate each message (needed for realistic compression).
- `-compressibility=N`: filler compresses ~N:1 under lz4. 1 = random. Default 1.

## How to run a benchmark
```
export KUBECONFIG=./kubeconfig.yaml
PODS=10 PARTITIONS=96 RF=1 ACKS=1 COMPRESSION=lz4 COMPRESSIBILITY=3 \
  DURATION=60s ./scripts/run-bench.sh
```
Env vars and defaults are documented in the script header. It cleans up the
topic even on error or Ctrl-C. The image is pinned by digest in
`manifests/kafka/producer-job.yaml`; rebuild + repush + repin if you change Go
code (registry `public.ecr.aws/data-on-eks/test`, ECR Public login region
us-east-1).

## Cluster facts
- Namespace `kafka`, cluster name `data-on-eks`, KRaft, plaintext listener 9092.
- In-cluster bootstrap: `data-on-eks-kafka-bootstrap.kafka.svc:9092`.
  Not reachable from a laptop (internal listener). Run producers in-cluster.
- Brokers on `r8g` (arm64) on-demand, one broker per node (pod anti-affinity).
- Broker volumes: `gp3-kafka` storage class, 500Gi, provisioned 1000 MB/s /
  10000 IOPS. The gp3 default of 125 MB/s is far too slow; provisioning is
  mandatory for high throughput.
- Tuned broker config: `num.network.threads=8`, `num.io.threads=16`,
  `num.replica.fetchers=8`, `replica.fetch.max.bytes=10485760`.
- Metrics: Kafka Exporter + JMX Prometheus are on. Measure rate externally.

## Key findings (measured)
- Brokers scale linearly: 3→6 brokers doubled durable throughput.
- Per-partition rate is disk-bound: ~135 MB/s on a 125 MB/s volume, ~683 MB/s
  on a 1000 MB/s volume (acks=1).
- Replication is a 3x disk tax. RF1 roughly doubled achieved throughput vs RF3
  and removed the acks=all timeout bottleneck.
- Compression only helps compressible data. lz4 on random data = 1.0 ratio.
  At compressibility 3 it measured 2.94:1; on-disk then ≈ logical under RF3.
- One 4-core producer pod delivers ~578 MB/s logical. Scale pods to the target.

## Broker count for 1M large records/s
| config | brokers |
|--------|---------|
| RF3, no compression | ~171 |
| RF3, 3:1 compression | ~57 |
| RF1, no compression | ~57 |
| RF1, 3:1 compression | ~19–24 (used 24) |

Sizing formula:
```
brokers ≈ (target_rec/s × avg_record_bytes × RF) ÷ (per_broker_disk_MBps × compression_ratio)
```

## Storage
1M/s at ~57 KB ≈ 205 TB/hour logical. RF1 + 3:1 ≈ 68 TB/hour (~2.85 TB/broker
for a 1-hour run). RF3, no compression ≈ 616 TB/hour. Size volumes for the full
run duration plus ~25% headroom; retention does not save you during a run.

## Gotchas
- Undersized disks fill fast and crash-loop a broker ("No space left"). Keep
  volumes large, delete topics after each run (run-bench.sh does this), and
  bound `-duration`.
- Do not set huge producer batches (8 MB hurt throughput). Defaults are fine.
- Changing broker config triggers a Strimzi rolling restart (one node at a
  time). Expect it and wait for `kafka Ready` before testing.
- Producer pods have anti-affinity vs brokers/controllers so they land on their
  own nodes. ~100 pods needs ~30 nodes; Karpenter provisions them.

## Cost and teardown (IMPORTANT)
Reduce brokers after completing work or hitting a roadblock (not after every
test). Current state: brokers scaled back to 6. Karpenter reclaims empty nodes
after `consolidateAfter: 600s` (~10 min).

To cut cost further:
```
export KUBECONFIG=./kubeconfig.yaml
# scale brokers down (edit replicas in manifests/kafka/node-pool-broker.yaml)
kubectl apply -f manifests/kafka/node-pool-broker.yaml
# or delete the whole cluster + node pools:
kubectl -n kafka delete kafka data-on-eks
kubectl -n kafka delete kafkanodepool broker controller
```
Broker volumes keep the provisioned 1000 MB/s / 10000 IOPS (the pricey part)
until the cluster or the volumes are removed.

---

# Phase 2: StarRocks real-time ingestion + Iceberg lakehouse tiering

## Goal
Use StarRocks as a real-time query engine on fresh Kafka data (30–60s
freshness). Move data older than 7 days to Iceberg tables on S3, cataloged in
AWS Glue. Query hot (StarRocks) and cold (Iceberg) data.

## Architecture (working end to end)
```
Kafka topic "events" ──Routine Load──> StarRocks hot table (fresh, day/hour partitioned)
                                             │
                                    scheduled tiering job (age > threshold)
                                             ▼
                        Iceberg table on S3, Glue catalog (cold, day-partitioned)
```

## What was built
- StarRocks cluster (operator `kube-starrocks` v1.11.3), namespace `starrocks`,
  cluster name `sr-bench`: 1 FE + 3 BE, shared-nothing.
- Each pod on its own dedicated `r8g.4xlarge` node (nodeSelector +
  pod anti-affinity + node-filling requests). FE query service:
  `sr-bench-fe-service:9030` (MySQL protocol, user `root`, no password).
- Hot table `bench.events`: DUPLICATE KEY(ingest_time, id),
  `PARTITION BY date_trunc('hour', ingest_time)`, `ingest_time DATETIME
  DEFAULT CURRENT_TIMESTAMP` (arrival time), columns id/name/active/score/
  tags(JSON)/meta(JSON)/note/filler(STRING), replication_num=1.
- Routine Load `rl_events`: JSON, jsonpaths for the fields, bootstrap
  `data-on-eks-kafka-bootstrap.kafka.svc:9092`, `max_batch_interval=10`.
  Measured freshness ~9–15s (well within 30–60s).
- Iceberg external catalog `iceberg_glue` (Glue metastore + S3), cold table
  `iceberg_glue.sr_ch_bench.events_cold` with **native** `PARTITION BY
  day(ingest_time)`, location `s3://sr-ch-bench-starrocks-data-c93eff82b8785d5a27e449d2a4/benchmark/events_cold`.
  tags/meta stored as STRING (Iceberg has no JSON type).
- Scheduled tiering: CronJob `sr-tiering` (mysql:8 pod). Computes one cutoff
  from StarRocks `now()`, INSERTs rows older than the cutoff into Iceberg,
  then DELETEs them from hot. Validation: `AGE="5 MINUTE"`, `*/5`. Production:
  `AGE="7 DAY"`, daily.
- Files: `manifests/starrocks/starrocks-cluster.yaml`,
  `hot-table-and-routine-load.sql`, `tiering-cronjob.yaml`,
  `iam-policy-starrocks.json`; `manifests/kafka/events-topic.yaml`,
  `events-producer-deployment.yaml`.

## Key decisions
- RF1 in StarRocks for the benchmark (throwaway data). Production uses RF3.
- Records keep the full ~57 KB text (real data), stored in StarRocks; only
  `_filler` in the synthetic generator represents that bulk text.
- Partition timestamp = arrival time (`CURRENT_TIMESTAMP`). For true event
  time, add a timestamp field to the producer.
- Cold storage: existing Glue DB `sr_ch_bench`, S3 bucket
  `sr-ch-bench-starrocks-data-c93eff82b8785d5a27e449d2a4` under `benchmark/`.

## Gotchas and how they were solved (important)
1. **Pod Identity credentials.** StarRocks pods must use the associated
   service account `starrocks-sa`. The operator's top-level `spec.serviceAccount`
   is deprecated/ignored in v1.11.3 — set it per component
   (`starRocksFeSpec.serviceAccount`, `starRocksBeSpec.serviceAccount`).
2. **AWS credential mode.** `aws.s3.use_instance_profile=true` uses the EC2
   IMDS provider, which pods cannot reach (hop limit) — it fails with
   "Failed to load credentials from IMDS". Use
   `aws.s3.use_aws_sdk_default_behavior=true` and
   `aws.glue.use_aws_sdk_default_behavior=true`, which use the AWS SDK v2
   default chain (includes the container-credentials provider = Pod Identity).
3. **Glue permissions.** The Pod Identity role needs Glue actions, not just S3.
   Complete policy in `manifests/starrocks/iam-policy-starrocks.json` (applied
   via Terraform by the user). Role `starrocks-618ef4e7b8f4ae37120fd785a9`.
4. **Iceberg day partitioning.** StarRocks 3.3.9's parser rejects
   `PARTITION BY day(col)` for Iceberg create (there is a TODO in the source);
   worked around with identity partitioning on a derived `dt DATE` column.
   **4.0.14** supports native transforms (`day`/`hour`/`month`/`bucket`/
   `truncate`) on the sink path (release notes 4.0–4.1). We upgraded (clean
   redeploy on 4.0.14 since data is throwaway; cleaned S3/Glue/PVCs first) and
   now use native `day(ingest_time)`.

## Ingest ceiling investigation (the main finding)
Question: how many BE nodes to ingest 1M rows/s of ~57 KB JSON via Routine Load?

- 1M × 57 KB = ~57 GB/s. The byte volume is the wall, not the row count.
- **Routine Load per-job task concurrency** =
  `min(partitionNum, desiredConcurrentNum, aliveBeNum, max_routine_load_task_concurrent_num)`
  (`KafkaRoutineLoadJob.calculateCurrentConcurrentTaskNum`). `aliveBeNum` caps
  it: 3 BEs → 3 tasks per job, regardless of desired/partitions.
- **Each task is parse-bound and single-threaded.** A task runs multiple Kafka
  consumers (`max_consumer_num_per_group`, BE config) that fetch in parallel,
  but they funnel into ONE queue → ONE pipe → ONE JSON parser; the tablet sink
  is then parallel (`max_load_dop`). For 57 KB JSON, the single-threaded parse
  pins ~1 core per task. Raising `max_consumer_num_per_group` 3→10 gave no gain
  (fetch was not the bottleneck).
- **Lever = more tasks per BE**, via multiple Routine Load jobs (partition
  sliced). A BE accepts up to `max_routine_load_task_num_per_be=16` tasks total.
- **Measured on 3 BEs (r8g.4xlarge), 48 partitions, backlog drain:**

  | jobs | tasks | tasks/BE | rows/s | MB/s logical | BE CPU |
  |------|-------|----------|--------|--------------|--------|
  | 1    | 3     | 1        | ~10,000 | ~570        | ~0.5 core |
  | 8    | 24    | 8        | ~30,000 | ~1,723      | ~2–4 cores |
  | 16   | 48    | 16 (cap) | ~42,000 | ~2,394      | (noisy) |

  Plateau ~42,000 rows/s on 3 BEs ≈ **~14,000 rows/s per BE (~800 MB/s logical)**.
  Sub-linear beyond 8 jobs (commit/txn/compaction overhead).
- **Node count for 1M rows/s via Routine Load ≈ ~70–100 BEs.**
  Caveat: per-BE figure is from 3 BEs; confirm by scaling BEs 3→6.

## Recommendation for large-scale ingest
Routine Load is simplest (built-in offset mgmt/recovery) but its parallelism is
capped by BE count and all JSON parsing lands on BEs. For 1M/s, prefer:
- **Kafka Connect (StarRocks sink connector)** or **Flink connector**:
  parallelism set by `tasks.max` on Connect/Flink workers (decoupled from BE
  count), can offload JSON→CSV transform to that tier, finer batch control
  (`bufferflush.maxbytes/intervalms`). Loads to StarRocks via Stream Load.
  Confirmed by StarRocks docs (`loading/kafka/Kafka-connector-starrocks.md`):
  recommended over Routine Load when you need format flexibility, CDC,
  multi-topic, or finer parallelism/throughput control.
- The BE write/compaction still bounds per-BE throughput, but you can fully
  saturate each BE and offload parsing → fewer StarRocks nodes than Routine
  Load on raw JSON. Not yet benchmarked here (proposed next step).

## Operational quick reference
```
export KUBECONFIG=./kubeconfig.yaml
FE="kubectl -n starrocks exec -i sr-bench-fe-0 -- mysql -h127.0.0.1 -P9030 -uroot"
echo "SHOW BACKENDS\G" | $FE                 # cluster health
echo "SELECT count(*),max(ingest_time) FROM bench.events;" | $FE   # hot + freshness
echo "SELECT count(*) FROM iceberg_glue.sr_ch_bench.events_cold;" | $FE  # cold
$FE < manifests/starrocks/hot-table-and-routine-load.sql   # (re)create hot pipeline
```

## Current state (as of this writing)
- StarRocks 4.0.14, 4 pods on 4 dedicated r8g.4xlarge nodes, Pod Identity OK.
- Producer `events-producer` at 12 replicas / goroutines=16 (raised for the
  ingest test). Tiering CronJob is **suspended**. `rl_events` and the sweep's
  `rlt_*` jobs are **stopped**. Hot table holds leftover test data.
- Cost: StarRocks (4 dedicated nodes) + 12 producer pods running. To reduce:
  restore producer to low rate, resume/keep tiering as needed, or scale the
  StarRocks cluster down.

## Remaining / next steps
- Benchmark Kafka Connect (or Flink) ingest to get the node count for the
  recommended architecture; compare to Routine Load ~14k rows/s/BE.
- Confirm Routine Load per-BE linearity by scaling BEs 3→6.
- Productionize tiering: `AGE="7 DAY"` + daily schedule; switch hot table to
  `date_trunc('day', ingest_time)` + `DROP PARTITION` eviction (atomic, avoids
  the predicate INSERT+DELETE idempotency gap); optional unified hot+cold view.

---

# Phase 2b: Kafka Connect (StarRocks sink) vs Routine Load — measured

## What was deployed
- Strimzi `KafkaConnect` cluster `sr-connect` (namespace kafka), image built from
  `quay.io/strimzi/kafka:0.47.0-kafka-3.9.0` + StarRocks connector **v1.0.6**
  (`kafka-connect/Dockerfile`, image digest
  `public.ecr.aws/data-on-eks/test@sha256:b418860e…`).
- `KafkaConnector` `starrocks-sink` → loads topic `events` into `bench.events`
  via Stream Load. `tasksMax: 48`. Manifests:
  `manifests/kafka/kafka-connect.yaml`, `manifests/kafka/starrocks-sink-connector.yaml`.

## Config gotchas (that cost time)
- The connector batches records as a **JSON array** → must set
  `sink.properties.strip_outer_array: "true"` (else "value is array type… set
  strip_outer_array=true").
- Each Stream Load JSON batch must stay under StarRocks' **100 MB json limit**.
  Keep `bufferflush.maxbytes` modest — **64 MB was stable**. Using 512 MB +
  `ignore_json_size=true` sent >100 MB loads that **OOM-killed both Connect
  workers and a BE** (heap + BE memory). Do not do that.
- Map `$._filler`→`filler` via `sink.properties.jsonpaths` + `.columns`;
  `ingest_time` takes its default.
- Cross-namespace: Connect (kafka ns) reaches FE `sr-bench-fe-service.starrocks.svc:8030`;
  Stream Load then redirects to a **BE on 8040** (pod-to-pod, cross-namespace) — worked.

## Result (both on the same 3 BEs)
| path | rows/s | ~MB/s logical | bottleneck |
|------|--------|---------------|------------|
| Routine Load (multi-job, 48 tasks) | ~42,000 | ~2,400 | BE JSON parse (BEs ~saturated) |
| Kafka Connect (3 workers, 48 tasks, stable) | ~23,600 | ~1,350 | Connect worker CPU; **BEs idle (~1.5 core)** |

## Why Kafka Connect was slower per core for JSON (source-grounded)
`StarRocksSinkTask.getRecordFromSinkRecord()` (checked-in source) for the JSON
path calls `jsonConverter.convertToJson(valueSchema, value).toString()` — it
**re-serializes** each record to JSON after the worker's `value.converter`
already **deserialized** it, and the BE then **parses** it again. So each 57 KB
payload is handled **three times** (worker deserialize → connector re-serialize
→ BE parse). Routine Load parses once, on the BE. Hence Routine Load is more
core-efficient for JSON, and the Connect bottleneck is the worker tier while the
BEs sit idle.

## Interpretation
- For **JSON-in-Kafka**, Kafka Connect did **not** reduce the node count — it
  needs more total cores (workers + BEs) for a given rate. Its real advantages:
  (a) the bottleneck (Connect tier) scales **independently** of the BEs;
  (b) format flexibility (Protobuf/CDC), multi-topic; (c) finer batch control.
- The **efficient** Connect path is CSV passthrough: `value.converter=StringConverter`
  + `sink.properties.format=csv` casts the value straight to String (the source
  does NOT re-serialize in the CSV branch) → the BE is the only parser, matching
  Routine Load, with independent Connect scaling. **Requires CSV in Kafka**, not
  JSON. Our data is JSON, so Routine Load stayed more efficient.
- Caveat: Connect was not pushed to a clean saturated ceiling — the aggressive
  buffer/`ignore_json_size` run destabilized workers and a BE. 23.6k is the
  stable 3-worker/64 MB number; scaling workers should raise it (BEs had
  headroom) but total cores would exceed Routine Load's for the same rate.

## Recommendation (updated)
- This JSON workload: **Routine Load (multi-job) is the more core-efficient
  ingester**; ~70–100 BEs for 1M rows/s (Phase 2 estimate).
- Choose **Kafka Connect** for its features/independent scaling, or if you can
  feed **CSV** (then it matches Routine Load efficiency via StringConverter
  passthrough). **Flink** connector (also Stream Load based) is worth evaluating
  when heavy transforms/parallelism are needed.

## State after this phase
- `KafkaConnector starrocks-sink` deleted (load stopped); `KafkaConnect sr-connect`
  cluster still deployed (6 worker pods) — delete to save cost:
  `kubectl -n kafka delete kafkaconnect sr-connect`.
- All 3 StarRocks BEs alive/recovered. Producer still 12 replicas. `bench.events`
  holds leftover data.

### Update: tuned 6-worker run + connector config quirks (measured)
Pushed Kafka Connect harder to find its ceiling on the same 3 BEs:
- 6 workers, 8 GB heap each, 48 tasks, backlog present (consumer lag ~64M, so
  NOT producer-limited).
- **Result: ~25,000 rows/s (~1.4 GB/s logical)** — barely above the 3-worker
  number, and **both tiers underutilized** (Connect ~9 cores across 6 workers,
  BEs ~6 cores across 3). So the limiter is the **connector's Stream Load path**,
  not CPU. Still well below Routine Load's ~42,000 rows/s on the same 3 BEs.

Connector config quirks that cost time (all real):
- `bufferflush.maxbytes` has a **hard minimum of 64 MB** (67108864). Below that
  the operator rejects the config (`PUT /connectors/... 400`), so the connector
  silently never registers (`/connectors` stays `[]`). Check the KafkaConnector
  `status.conditions` for the validation error.
- The Stream Load SDK sends **~128 MB chunks** regardless of `bufferflush.*`,
  which exceeds StarRocks' **100 MB JSON** Stream Load limit for large records.
  So large-JSON loads fail unless `sink.properties.ignore_json_size: "true"` is
  set. With big buffers that also risks BE OOM (we crashed a BE once); keep the
  buffer at the 64 MB minimum.
- Net: for large JSON records the StarRocks Kafka connector is awkward and
  *slower* than Routine Load; its throughput here was capped by the load path,
  not by CPU on either tier.

### Final comparison (same 3 BEs, ~57 KB JSON records)
| ingester | throughput | limiter | notes |
|----------|-----------|---------|-------|
| Routine Load (multi-job, 48 tasks) | **~42,000 rows/s** | BE JSON parse | native, simplest ops |
| Kafka Connect (3–6 workers, 48 tasks) | **~23–25,000 rows/s** | connector Stream Load path (128 MB JSON chunks vs 100 MB limit; per-task serial loads) | needs ignore_json_size; BEs underused |

**Conclusion:** For this JSON-in-Kafka workload, **Routine Load is both simpler
and faster per BE**. Kafka Connect's theoretical advantage (independent tier
scaling, format flexibility, CDC, multi-topic) did not translate into higher
throughput here because the JSON path re-serializes on the workers and the SDK's
128 MB chunks fight the 100 MB JSON limit. Kafka Connect would win with **CSV**
data (StringConverter passthrough, no re-serialization, no JSON size cap) or when
its features are required. For pure JSON throughput, stay on Routine Load
(~70–100 BEs for 1M rows/s); or evaluate Flink for heavy transforms.

### Node placement (all benchmark workloads)
All benchmark pods are pinned to **on-demand** nodes with the
`karpenter.sh/do-not-disrupt: "true"` annotation, so spot interruption / Karpenter
consolidation cannot disrupt them mid-run:
- Kafka brokers/controllers: Kafka CR (nodeAffinity capacity-type on-demand +
  do-not-disrupt) — pre-existing.
- StarRocks FE/BE: `nodeSelector` adds `karpenter.sh/capacity-type: on-demand`
  (with `node.kubernetes.io/instance-type: r8g.4xlarge`) and the `annotations`
  field adds do-not-disrupt (per component in the CR).
- Producer Deployment and Kafka Connect: nodeSelector/nodeAffinity on-demand +
  do-not-disrupt annotation.

### Phase 2c: 12-BE Routine Load scaling test — METHODOLOGY FINDING (important)
Scaled StarRocks BEs 3 -> 12 (12 dedicated on-demand r8g.4xlarge), events topic
-> 192 partitions, table recreated with 48 buckets (tablet contention fix, see
below), 12 partition-sliced Routine Load jobs (144 tasks = 12/BE) draining the
backlog from OFFSET_BEGINNING.

**Result: sustained ~12,500 rows/s with the BEs IDLE (~0.4 core/BE).** This is
NOT the StarRocks ingest ceiling — it is the **Kafka cold-backlog read rate**.
- `OFFSET_BEGINNING` reads the oldest *retained* data off the RF1 gp3 broker
  volumes. 144 tasks each read 1 partition => ~24 concurrent cold read-streams
  per broker => disk-I/O-bound. Broker CPU stayed low (0.2-0.75 cores) and BE CPU
  stayed idle (~0.4/BE). An initial ~122k "burst" was page-cache/buffered data,
  collapsing to ~12.5k once reads went to disk.
- Meanwhile the producer keeps adding ~20k hot msgs/s, so lag grows.

**Implications for the 1M rows/s goal:**
1. The backlog-drain method under-measures BE capacity — the BEs never get fed.
   The earlier 3-BE ~42k was effectively hot-fed (data in page cache); it reflects
   BE/consume capacity better than cold drain does.
2. To measure OR achieve high sustained ingest you must serve data **hot** from
   Kafka page cache: producer feeding at >= target rate, enough partitions, and
   enough Kafka read parallelism (more brokers, faster/again RF>1 storage). At
   scale the bottleneck can be **Kafka broker read I/O**, not StarRocks BEs.
3. Correct next test: scale the producer well above the target (e.g. >150k msg/s,
   as in Phase 1's 1M/s run) and have the jobs **tail OFFSET_END**, so Kafka
   serves from RAM; then the StarRocks BE ceiling and its per-BE linearity can be
   measured cleanly.

**Secondary finding — tablet contention:** with only 12 buckets, 192 load tasks
caused a publish-version **abort storm** (`abortedTaskNum` ~48 vs
`committedTaskNum` ~3, throughput collapsed). Recreating the table with 48 buckets
(~4 tablets/BE) removed the aborts. Rule: scale `DISTRIBUTED BY ... BUCKETS` with
BE count and load concurrency (~3-4 tablets per BE, and keep tasks-per-tablet low).

Also raised FE dynamic config `max_routine_load_task_concurrent_num` 5 -> 12 so a
single job can use up to aliveBE tasks.

### Phase 2d: 12-BE corrected HOT-feed test — StarRocks is NOT the bottleneck
Boosted the producer (16 pods, 4 cores, 48 goroutines each) to feed hot data at
~85k msgs/s (~4.9 GB/s), recreated 12 jobs tailing **OFFSET_END** (144 tasks,
48-bucket table) so Kafka serves recent data from page cache.

**Result: ~56,000 rows/s ingested (~3.2 GB/s) with every compute tier idle:**
- StarRocks BE CPU: ~1.4 of 16 cores per BE (**~9% utilized**).
- Kafka broker CPU: ~1.9 cores/broker; broker node CPU ~22%; broker RAM 15-42%.
- Consume (56k) < produce (85k), so it is consume/read-path limited — but NOT by
  StarRocks compute and NOT by broker CPU/RAM.

**Interpretation:** the binding resource is **Kafka broker storage bandwidth**.
The records are large (~57 KB); at these rates the RF1 gp3 broker volumes
(provisioned 1000 MB/s each, 6 brokers) are saturated by write (~4.9 GB/s) plus
the consumer reads. Every compute tier (StarRocks BEs, broker CPU) has large
headroom.

**Key conclusion for the 1M rows/s goal:**
- Across all tests (cold drain 12.5k, hot feed 56k), **we have never saturated the
  StarRocks BEs** — BE CPU stayed <10%. StarRocks has substantial unused capacity;
  the earlier "~70-100 BEs for 1M/s" estimate was derived from a Kafka-bound
  measurement and almost certainly **overstates** the BE count needed.
- The real scaling constraint for this large-record workload is the **Kafka
  ingest/storage tier**: broker count, per-broker storage bandwidth/IOPS,
  replication factor, and partition/read parallelism. Size Kafka first.
- To pin StarRocks' true per-BE ceiling, remove the Kafka limit: scale the broker
  pool (e.g. 6 -> 18-24, as in the Phase 1 1M/s run) and/or faster storage, keep
  the hot feed above the target, then re-measure. Only then will BE CPU rise
  enough to read a real per-BE number.

FE config for reference: `max_routine_load_task_concurrent_num=12`,
`max_routine_load_task_num_per_be=16`, `max_routine_load_batch_size=4GB`,
`routine_load_task_consume_second=15`. Table `bench.events` now 48 buckets.

### Phase 2e: r8g.12xlarge brokers retest — StarRocks STILL not the bottleneck
Moved the 6 brokers to r8g.12xlarge (sustained network + EBS baseline > gp3's
1000 MB/s cap; controllers kept on r8g.4xlarge via a broker-scoped node-pool
template). Re-ran the hot feed (producer up to 24 pods, 16 tail jobs = 192 tasks
= 16/BE, 48-bucket table).

Measured (12 BEs throughout; BE = StarRocks backend CPU out of 16 cores each):
| scenario | produce | ingest | BE CPU |
|---|---|---|---|
| 4xlarge brokers, hot, 12/BE | ~85k/s | ~56k rows/s | ~9% |
| 12xlarge brokers, hot, 16/BE | ~125k/s | ~64k rows/s | ~10-15% |
| 12xlarge brokers, **pure drain** (producer off, hot cache) | 0 | **~80k rows/s** | **~5%** |

Findings:
- Broker upgrade lifted **produce** 85k -> 125k/s (writes were instance-throttled
  on 4xlarge's 625 MB/s EBS baseline; 12xlarge removes that).
- **Ingest only rose 56k -> 64k** with writes, and to ~80k with writes stopped.
  The write/read contention on the single **gp3 1000 MB/s volume** per broker is
  one active limit (produce ~1183 MB/s/broker + consumer reads > 1000 MB/s cap).
- **BE CPU never exceeded ~15%, and was ~5% at the 80k pure-drain peak.** Across
  every configuration tested (cold 12.5k, hot 56/64k, drain 80k) StarRocks BEs
  have been essentially idle. **We have never come close to the StarRocks ceiling.**
- At 80k with writes off and reads from page cache (so not disk-read-bound, not
  BE-bound, not network-bound on 12xlarge), the remaining limit is the **Routine
  Load consume path**: task parallelism is capped at the **partition count**
  (192 partitions -> 192 tasks -> ~415 rows/s/task). More consume throughput
  needs **more partitions** (more tasks), not more BEs.

**Bottleneck ladder (for this ~57 KB-record workload), in the order we hit them:**
1. Kafka connector JSON path / cold-backlog disk reads (earlier phases).
2. r8g.4xlarge broker burstable EBS/network baselines (625 MB/s) -> fixed by 12xlarge.
3. gp3 per-volume 1000 MB/s cap (write+read contention) -> next fix: JBOD multi-volume or io2.
4. Routine Load task parallelism = partition count -> fix: more partitions.
StarRocks BE compute is NOT on this ladder yet (idle throughout).

**Implication for 1M rows/s:** StarRocks needs FAR fewer BEs than the original
~70-100 estimate (BEs were ~5% busy at 80k rows/s). The build-out is a **Kafka +
partition/parallelism** exercise: enough brokers with aggregated storage bandwidth
(JBOD/io2, or local-NVMe r8gd), and enough partitions to give Routine Load the
task parallelism. Provision the ingest pipeline first; StarRocks compute is cheap
here.

### Phase 2f: r8gd.12xlarge local-NVMe brokers + 384 partitions
Switched the 6 brokers to r8gd.12xlarge with **local NVMe** (Strimzi ephemeral
storage on Karpenter's RAID0 instance store — 2.6 TB per broker; controllers left
on r8g.4xlarge). Recreated the topic at **384 partitions**, raised
`max_routine_load_task_num_per_be` 16 -> 32, ran 32 tail jobs = **384 tasks
(32/BE)**, producer at 32 pods.

Measured (12 BEs, BE CPU out of 16 cores each):
| config | produce | ingest | BE CPU |
|---|---|---|---|
| 4xlarge EBS brokers, 16/BE | ~85k/s | ~56k rows/s | ~9% |
| 12xlarge EBS brokers, 16/BE | ~125k/s | ~64k rows/s | ~10-15% |
| **r8gd.12xlarge NVMe, 32/BE** | **~152-160k/s** | **~101k rows/s (~5.8 GB/s)** | **~13%** |

Findings:
- **NVMe removed the storage bottleneck:** produce rose to ~152k/s (~8.7 GB/s =
  ~1,443 MB/s/broker, far past gp3's 1000 MB/s cap and the 4xlarge 625 MB/s
  baseline). Broker logs on the 2.6 TB RAID0 NVMe (`/var/lib/kafka`).
- **Ingest scaled with task/partition count:** 192 tasks -> 384 tasks lifted
  consume 64k -> ~101k rows/s (~1.6x; per-task rate dips slightly with contention).
- **StarRocks BEs STILL not saturated (~13% at 101k rows/s).** Consume (101k) <
  produce (160k), so still consume-path limited (Routine Load task parallelism /
  per-task rate / FE coordination), NOT BE compute, NOT Kafka storage/network.

**Where we are now (bottleneck ladder, updated):**
1-2. Connector JSON path / 4xlarge burst baselines — fixed.
3. gp3 1000 MB/s volume cap — removed by NVMe (r8gd).
4. Routine Load consume parallelism (= partition/task count) — the current limit;
   scales ~linearly with tasks. BEs remain idle.
StarRocks BE compute is STILL not the constraint anywhere.

**Best sustained StarRocks ingest measured: ~101,000 rows/s (~5.8 GB/s) on 12 BEs
at ~13% CPU.** Extrapolation: BE compute headroom suggests a single 12-BE cluster
could reach several hundred k rows/s if fed with enough partitions/tasks; NVMe
brokers supply the Kafka bandwidth. For 1M rows/s: keep scaling partitions + tasks
(and add BEs only once they actually approach saturation), with r8gd NVMe brokers
(RF>=2 + tiered storage in production) for the ingest bandwidth. StarRocks compute
stays cheap.

### Phase 2g: FAIR Kafka Connect vs Routine Load (both hot, NVMe cluster) — REVERSAL
Also moved StarRocks BEs to **r8gd.4xlarge with local NVMe** (hostPath on the
RAID0 array at /mnt/k8s-disks/0; BE data on /dev/md127, 885 GB). Redeployed Kafka
Connect (6 workers, 8 GB heap, 48 tasks) and ran it hot against the live producer
on the same NVMe cluster used for the Routine Load 101k test.

Measured (12 BEs on r8gd.4xlarge, NVMe brokers, hot feed):
| ingester | ingest | limiter | BE CPU |
|---|---|---|---|
| Routine Load (384 tasks / 32 BE) | ~101k rows/s | consume parallelism (partitions/tasks) | ~13% |
| **Kafka Connect (6 workers / 48 tasks)** | **~155-160k rows/s (~9 GB/s)** | **Connect worker CPU (~87% of 48 cores)** | ~17% |

- At producer 159k/s Connect kept pace (159,794 rows/s). Pushed to producer 233k/s,
  Connect capped at ~154k with workers ~42/48 cores -> **Connect-worker-CPU-bound**.

**This REVERSES the Phase 2/2b conclusion.** The earlier result (Connect ~23-25k,
"half of Routine Load") was an artifact of the **storage bottleneck**: the Connect
tests drained cold backlog from disk-bound r8g.4xlarge gp3 brokers, starving the
workers, and that was mis-attributed to the connector's JSON path. On a properly
provisioned cluster (NVMe brokers + NVMe BEs, hot feed):
- **Kafka Connect (~155-160k) BEAT Routine Load (~101k)** for this JSON workload.
- Connect parallelizes the JSON deserialize/re-serialize across **dedicated worker
  CPUs** and ships large Stream Load batches; it scales by **adding workers**
  (stateless, easy). It was worker-CPU-bound, not BE-bound (BE ~17%).
- Routine Load pushes parse onto the BEs but is capped by **per-task consume
  parallelism = partition count**; BEs stayed ~13%.
- The large-JSON caveat still applies (needs `ignore_json_size`, 64 MB buffer); on
  NVMe BEs it ran clean with no OOM (BE ~17%).

**Revised guidance:** For high-throughput JSON ingest on well-provisioned Kafka,
**Kafka Connect is competitive-to-better than Routine Load** and scales more simply
(add stateless workers). Earlier "Routine Load is faster" was wrong — it was
confounded by broker storage I/O. For 1M rows/s, either path works; the gating
factors are Kafka bandwidth (NVMe brokers) + enough parallelism (Connect workers OR
RL partitions/tasks). StarRocks BE compute remained <20% in all cases.

### Phase 2h: 500k target — Routine Load HITS IT (single-AZ NVMe cluster)
Rebuilt everything in a single AZ (us-west-2c) to fix rack-aware partition skew
(a lone broker in one AZ was getting 192/384 partitions) and eliminate cross-AZ
traffic: 12 brokers (r8gd.12xlarge NVMe), 3 controllers, 3 FEs, 12 BEs
(r8gd.4xlarge, BE storage on local NVMe hostPath /mnt/k8s-disks/0). Topic events
= 384 partitions, perfectly even (32/broker). Producer ~578-622k msgs/s.

**Kafka Connect on this clean cluster still capped ~150-200k** (JSON whole-batch
in-memory parse on BE + connector buffer has no real backpressure; bigger batches
are worse). Root-caused the 100 MB limit to BE config
`streaming_load_max_batch_size_mb` (raised to 1024 at runtime -> loads succeed
without ignore_json_size, but throughput didn't improve). So Connect+JSON is the
wrong tool for this.

**Routine Load: ~600,000 rows/s (~34 GB/s), BEs ~62% CPU (120/192 cores).**
- Config: 32 partition-sliced jobs x 12 tasks = 384 tasks (32/BE);
  `max_routine_load_task_concurrent_num=12`, `max_routine_load_task_num_per_be=32`.
- **500k target EXCEEDED.** RL wins decisively over Connect for large-JSON because
  it **streams** the JSON parse inside the BEs (no whole-batch alloc, no Stream
  Load HTTP funnel, no connector buffer) and parallelizes across BE cores. BEs
  were finally the working tier (~62%) vs <20% under Connect.
- This overturns the Phase 2g "Connect beats RL": that was because RL had too few
  tasks (per-BE cap 16 -> 192 tasks) on a multi-AZ/skewed topic. Given enough task
  parallelism on a clean cluster, RL >> Connect here.

**DEFINITIVE clean sustained run (from empty disk): ~525,885 rows/s (~30 GB/s)
over 134s, 32/32 jobs RUNNING the whole time, BEs ~60% CPU, disk 3%->11%.**
500k target ACHIEVED and STABLE.

**Stability root cause (understood):** the first attempts destabilized because
(a) the run started with a full table (166M leftover rows) + async TRUNCATE, so BE
local NVMe filled fast -> **node DiskPressure -> kubelet evicted BEs** -> RF1 data
loss -> RL jobs paused (offset out of range). BE data is on hostPath sharing the
kubelet NVMe (/mnt/k8s-disks/0), so tablet data counts against node ephemeral disk.
From a CLEAN/empty disk it runs stably (~526k) for ~10 min before disk fills.
No broker restarts; not an RL throughput limit.

**For durable, indefinite 500k+ in production:**
- **RF>=2** on both Kafka topic and StarRocks table (survive a broker/BE loss).
- **Dedicated, larger BE data volumes** separate from the kubelet disk (avoid
  DiskPressure evictions), or continuous compaction/retention headroom.
- Kafka topic retention sized so consumers can't fall behind into deleted segments.

NOTE: BE `streaming_load_max_batch_size_mb=1024` and the RL FE configs are runtime
only (not persisted to be.conf/fe.conf) — reset on restart.

### Phase 2i: 1M push — blocked by Kafka RF1 fragility (NOT StarRocks)
Persisted the tuned configs first (survives restarts): ConfigMaps `be-conf`/`fe-conf`
referenced via CR `configMapInfo` (manifests/starrocks/conf/{be,fe}.conf) ->
`streaming_load_max_batch_size_mb=1024`, `max_routine_load_task_concurrent_num=24`,
`max_routine_load_task_num_per_be=32`. Verified active after FE/BE roll.

Scaled to **24 brokers (r8gd.12xlarge) + 24 BEs (r8gd.4xlarge) + 3 FEs, single-AZ
us-west-2c**, topic 768 partitions, 32 jobs x 24 = 768 tasks, producer up to 96
pods (bursts ~3M msgs/s). Provisioning had no capacity issues.

**Result: could NOT hold a stable 1M window — RL jobs repeatedly pause. Root cause
= Kafka RF1 fragility at 24 brokers, not StarRocks throughput:**
- The 24-broker cluster keeps having a few brokers **fenced/flapping** in KRaft;
  under **RF1** their partitions go **leaderless (Leader: none)** — ~32-35 dead
  partitions at any time. RL tasks on those partitions fail with "Failed to get
  kafka partition info in BE" -> job pauses -> cascade.
- Over-producing made it worse: producer bursts (1.3-3M/s) churn the RF1 topic;
  lagging tasks hit "offset out of range". Throttling to ~900k-1.1M helped but the
  leaderless-partition problem persists (independent of retention — unlimited
  retention did not fix it).
- **When jobs run, BEs sit at only ~20-25% CPU** — StarRocks has large headroom for
  1M; the bottleneck is entirely the fragile RF1 Kafka tier.

**Fix for stable 1M (production-correct): RF>=2 on the Kafka topic** so a
fenced/flapping broker doesn't kill partitions (a replica takes leadership). Also
RF>=2 / more disk headroom on StarRocks (BE ephemeral NVMe + RF1 = data loss on
eviction, seen earlier). Tradeoff: RF2 ~doubles broker write/replication load.

**Bottom line:** 500k is proven and durable (~526k clean sustained). 1M is
achievable on StarRocks (BE headroom confirmed) but requires a **replicated (RF>=2)
Kafka tier** for stability — RF1 at 24 brokers is too fragile to hold the window.

### Phase 2j: 1,000,000 rows/s ACHIEVED (single-AZ NVMe, sharded tables)
After a full clean Kafka rebuild (21 r8gd.12xlarge NVMe brokers, all on the
memory-optimized-graviton nodepool so every broker gets the RAID0 NVMe — see
scheduling notes below), 24 BEs (r8gd.4xlarge NVMe), 768-partition topic:

**Peak sustained: ~1,103,086 rows/s (~63 GB/s) — RL keeping up with a ~1.1M
producer, BEs at ~61%, load-txn aborts ~2%.** 1M target EXCEEDED.

**The real throughput blocker was SINGLE-TABLE transaction contention** (the key
insight): with 768 RL tasks committing every 10s into ONE table (bench.events,
192 tablets), StarRocks' per-table load-transaction publish path saturated above
~900k -> ~50% of task transactions ABORTED (errorRows=0, pure txn aborts) -> BEs
burned CPU on rolled-back work -> ingest collapsed to ~330k. Making batches larger
(max_batch_interval 10->30s) made it WORSE (longer-held txns contend more).
**Fix: shard the ingest across 32 tables (one per job, ~24 committers each instead
of 768).** Abort rate dropped ~50% -> ~2%, ingest jumped to 1.1M.

**Bottleneck ladder (final):**
1. Connector JSON path (~200k) -> use Routine Load.
2. Broker EBS/network baselines (4xlarge) -> r8gd.12xlarge NVMe.
3. gp3 volume cap / cold-read -> NVMe brokers.
4. RL consume parallelism -> more partitions/tasks (768).
5. **Single-table load-txn contention -> shard across many tables.** <- the 1M unlock
StarRocks BE CPU never exceeded ~65% at 1.1M -> more headroom exists.

**Durability limit (same as 500k, hit faster at 1.1M):** sustaining ~1.1M fills the
BE local NVMe (tablet data on hostPath, shared with kubelet ephemeral) within
minutes -> node DiskPressure -> BE evicted -> under RF1 tablets lost -> RL pauses
("Tablet lost replicas"). Evicted BEs can't reschedule (node gets a disk-pressure
taint; hostPath data persists). Also set Kafka retention.ms=-1 during a test which
filled broker NVMe too (reverted to bounded).

**For a DURABLE production 1M/s:**
- **RF>=2** on Kafka topic AND StarRocks table (survive a broker/BE loss; no
  single-eviction cascade).
- **Dedicated, sized BE data volumes** separate from kubelet ephemeral (avoid
  DiskPressure evictions), with capacity for the retention window + compaction.
- **Shard writes across multiple tables** (or partitions) to spread load-txn
  contention -- one table caps ~900k here.
- Bounded Kafka retention sized to the consumer lag window.
- All broker/BE nodes on a nodepool that RAID-mounts local NVMe
  (memory-optimized-graviton), pinned via karpenter.sh/nodepool.

Scheduling gotcha found: pods pinned to a single instance-type + single nodepool +
single AZ + per-node anti-affinity can wedge Karpenter/YuniKorn provisioning of the
Nth dedicated node ("no instance type met all requirements" -- not capacity). A
clean rebuild placed all 21 brokers on NVMe uniformly.
