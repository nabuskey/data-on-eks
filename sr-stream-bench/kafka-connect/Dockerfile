# Kafka Connect image with the StarRocks sink connector plugin.
# Base MUST be the Strimzi Kafka image matching the installed operator, so the
# Strimzi KafkaConnect CR can manage it. Get the exact tag from a broker pod:
#   kubectl -n kafka get pod data-on-eks-broker-0 -o jsonpath='{.spec.containers[0].image}'
# Then build:
#   docker buildx build --builder mp-builder --platform linux/amd64,linux/arm64 \
#     --build-arg BASE=<that image> \
#     -t public.ecr.aws/data-on-eks/test:connect-1.0.6 --push kafka-connect/
ARG BASE=quay.io/strimzi/kafka:latest-kafka-3.9.0
FROM ${BASE}
USER root:root
RUN mkdir -p /opt/kafka/plugins/starrocks
COPY starrocks-connector-for-kafka-1.0.6-with-dependencies.jar /opt/kafka/plugins/starrocks/
USER 1001
