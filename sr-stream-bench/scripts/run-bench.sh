#!/usr/bin/env bash
#
# run-bench.sh runs one Kafka producer benchmark.
# It creates the topic, runs the producer pods, prints the total rate, and
# always deletes the topic at the end. The delete runs even on error or
# interrupt, so no test data is left on the brokers.
#
# Set the run with environment variables. Defaults are in brackets.
#   TOPIC           topic name [bench]
#   PARTITIONS      number of partitions [48]
#   RF              replication factor [3]
#   PODS            number of producer pods [2]
#   GOROUTINES      goroutines per pod [128]
#   CPUS            CPUs per pod [4]
#   ACKS            producer acks: 0, 1, -1 [-1]
#   COMPRESSION     none, gzip, snappy, lz4, zstd [lz4]
#   COMPRESSIBILITY target lz4 ratio of the filler [3]
#   REUSE           reuse the record bytes [false]
#   DURATION        run time [60s]
#   MINSIZE         min record size in bytes [10240]
#   MAXSIZE         max record size in bytes [102400]
#   BATCH_BYTES     max bytes per batch [1048576]
#   BATCH_SIZE      max messages per batch [100]
#   LINGER          batch wait time [10ms]
#
# Example:
#   PODS=10 PARTITIONS=96 COMPRESSIBILITY=3 ./scripts/run-bench.sh

set -euo pipefail

NS=kafka
BASE="$(dirname "$0")/../manifests/kafka/producer-job.yaml"

TOPIC=${TOPIC:-bench}
PARTITIONS=${PARTITIONS:-48}
RF=${RF:-3}
PODS=${PODS:-2}
GOROUTINES=${GOROUTINES:-128}
CPUS=${CPUS:-4}
ACKS=${ACKS:--1}
COMPRESSION=${COMPRESSION:-lz4}
COMPRESSIBILITY=${COMPRESSIBILITY:-3}
REUSE=${REUSE:-false}
DURATION=${DURATION:-60s}
MINSIZE=${MINSIZE:-10240}
MAXSIZE=${MAXSIZE:-102400}
BATCH_BYTES=${BATCH_BYTES:-1048576}
BATCH_SIZE=${BATCH_SIZE:-100}
LINGER=${LINGER:-10ms}

RUN="rb-$(date +%s)"
BOOTSTRAP="data-on-eks-kafka-bootstrap.${NS}.svc:9092"

cleanup() {
  echo "--- cleanup: deleting jobs and topic ${TOPIC} ---"
  kubectl -n "$NS" delete job -l "runbench=${RUN}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "$NS" delete kafkatopic "$TOPIC" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

echo "--- create topic ${TOPIC} (${PARTITIONS} partitions, RF ${RF}) ---"
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaTopic
metadata:
  name: ${TOPIC}
  namespace: ${NS}
  labels:
    strimzi.io/cluster: data-on-eks
spec:
  partitions: ${PARTITIONS}
  replicas: ${RF}
  config:
    retention.ms: 3600000
    max.message.bytes: 10485760
EOF
kubectl -n "$NS" wait "kafkatopic/${TOPIC}" --for=condition=Ready --timeout=120s

echo "--- launch ${PODS} producer pods ---"
for i in $(seq 1 "$PODS"); do
  n="${RUN}-${i}"
  sed -e "s/^  name: kafka-bench-producer/  name: ${n}/" \
      -e "s/^      labels:/      labels:\n        runbench: ${RUN}/" \
      -e "s#-topic-file=bench,/schema/sample.json#-topic-file=${TOPIC},/schema/sample.json#" \
      -e "s/-goroutines=1/-goroutines=${GOROUTINES}/" \
      -e "s/-cpus=1/-cpus=${CPUS}/" \
      -e "s/-acks=1/-acks=${ACKS}/" \
      -e "s#-kafka-url=data-on-eks-kafka-bootstrap.kafka.svc:9092#-kafka-url=${BOOTSTRAP}#" \
      -e "s/-min-size=10240/-min-size=${MINSIZE}/" \
      -e "s/-max-size=102400/-max-size=${MAXSIZE}/" \
      -e "s/-duration=60s/-duration=${DURATION}/" \
      -e "s/-compression=lz4/-compression=${COMPRESSION}/" \
      -e "s#            - \"-reuse=true\"#            - \"-reuse=${REUSE}\"\n            - \"-compressibility=${COMPRESSIBILITY}\"\n            - \"-batch-bytes=${BATCH_BYTES}\"\n            - \"-batch-size=${BATCH_SIZE}\"\n            - \"-linger=${LINGER}\"#" \
      -e 's/cpu: "1"/cpu: "'"${CPUS}"'"/g' \
      "$BASE" | kubectl apply -f - >/dev/null
done

echo "--- waiting for completion ---"
jobs=""
for i in $(seq 1 "$PODS"); do jobs="$jobs job/${RUN}-${i}"; done
# shellcheck disable=SC2086
kubectl -n "$NS" wait --for=condition=complete $jobs --timeout=1200s

tot_b=0; tot_m=0
for i in $(seq 1 "$PODS"); do
  line=$(kubectl -n "$NS" logs "job/${RUN}-${i}" 2>&1 | tail -1)
  b=$(echo "$line" | grep -o 'bytes=[0-9]*' | cut -d= -f2)
  m=$(echo "$line" | grep -o 'messages=[0-9]*' | cut -d= -f2)
  tot_b=$((tot_b + ${b:-0})); tot_m=$((tot_m + ${m:-0}))
done

secs=$(echo "$DURATION" | sed 's/s$//')
python3 -c "
b=$tot_b; m=$tot_m; s=$secs
print(f'RESULT topic=${TOPIC} pods=${PODS} parts=${PARTITIONS} acks=${ACKS} comp=${COMPRESSION} cr=${COMPRESSIBILITY}')
print(f'  messages={m:,}  logical={b/1e9:.1f}GB  =>  {m/s:,.0f} rec/s  {b/s/1e6:,.0f} MB/s logical')
"
echo "--- under-replicated partitions during/after (empty=healthy) ---"
urp=$(kubectl -n "$NS" exec data-on-eks-broker-0 -- bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe --topic "$TOPIC" --under-replicated-partitions 2>/dev/null | grep -v Defaulted | grep -c Topic || true)
echo "$urp"
# cleanup runs on EXIT
