# Install

## Cluster
1. `terraform apply`
2. `k apply -f karpenter.yaml`
3. Scale up EBS benchmark managed node group to 6 nodes.

## Celeborn

Two available configurations for Celeborn in Karpenter. This assumes running in us-west-2. Change the region in `karpenter.yaml` if running in another region.

1. `shuffling-service`. r6i.4xlarge with 1000 GB EBS for shuffling data.
2. `shuffling-service-nvme` r6id.4xlarge 960 GB NVME ephemeral storage for shuffling data.

Install Celeborn with helm. It uses a custom built image because there's no official looking image available.

```
git clone git@github.com:apache/celeborn.git
cd celeborn && git checkout v0.5.4 && cd -
helm upgrade  -n celeborn celeborn ./cleborn/charts/celeborn -f values.yaml
```

# Generate data

### 3 TB

1. `k apply -f tpcds-benchmark-data-generation-3t.yaml`


# Run Benchmarks

3 iterations per benchmark configurations. Be sure to update the S3 bucket locations.

1. Default shuffler. `k apply -f tpcds-benchmark-3t-ebs-iteration-3.yaml`
2. Celeborn. `k apply -f tpcds-benchmark-3t-ebs-celeborn-iteration-3.yaml`

# Gather results

Results should be under `<S3_BUCKET>/TPCDS-TEST-3T-RESULT/<EPOCH_TIME>`. Look for the CSV folder for breakdown of how long each query took.

