## Datafusion Comet TPCDS Benchmark Results


### Setup

#### Versions

1. EKS Cluster `1.34`
2. Spark `3.5.7`
3. DataFusion Comet `0.13.0`

#### Infra

1. 24 `c5d.12xlarge` nodes. (48 cores, 96 GB memory, 2 x 900 NVME SSD each)
2. 23 executors. 58 GB memory. One pod per node. No other pods except for essential daemon sets.
3. All nodes in the same availability zone.
4. Data stored in a S3 bucket with parquet format.



#### Configurations

Configurations are identical except for the following Comet specific settings:

```yaml
"spark.memory.offHeap.enabled": "true"
"spark.memory.offHeap.size": "32g"
"spark.comet.exec.enabled": "true"
"spark.plugins": "org.apache.spark.CometPlugin"
"spark.shuffle.manager": "org.apache.spark.sql.comet.execution.shuffle.CometShuffleManager"
"spark.comet.explainFallback.enabled": "true"
"spark.comet.exec.shuffle.enabled": "true"
"spark.comet.cast.allowIncompatible": "true"
"spark.comet.exec.shuffle.mode": "auto"
"spark.hadoop.fs.s3a.endpoint.region": "us-west-2"
```

#### Benchmark

1. 3TB data size
2. Setup from: https://github.com/aws-samples/emr-on-eks-benchmark

### Results

#### Overall

Overall Datafusion Comet performed worse than the default execution engine (~18% slower).

| Name | Completion Time Seconds | Performance |
| --- | --- | --- |
| Default | 2090.458718919 | Baseline |
| Comet | 2470.427600079 | -18% |


#### Per Query

Performance is query dependent. Some performing much better while others performing much worse.


Top Performing

| Name | Percent Improvement |
| --- | --- |
| q8-v2.4 | 54 |
| q5-v2.4 | 47 |
| q41-v2.4 | 40 |
| q93-v2.4 | 40 |
| q9-v2.4 | 34 |
| q76-v2.4 | 34 |
| q90-v2.4 | 33 |
| q73-v2.4 | 32 |
| q97-v2.4 | 31 |
| q44-v2.4 | 30 |


Bottom Performing

| Name | Percent Improvement |
| --- | --- |
| q25-v2.4 | -1308 |
| q17-v2.4 | -1020 |
| q54-v2.4 | -586 |
| q29-v2.4 | -330 |
| q45-v2.4 | -316 |
| q6-v2.4 | -210 |
| q18-v2.4 | -199 |
| q68-v2.4 | -168 |
| q11-v2.4 | -162 |
| q74-v2.4 | -137 |




### Observations



#### Cannot reliably determine AWS region out of the box

You must set `spark.hadoop.fs.s3a.endpoint.region` otherwise HTTP HEAD request is sent and it sometimes fails to determine the region.

```
org.apache.comet.CometNativeException: General execution error with reason: Generic S3 error: Failed to resolve region: error sending request for url
at org.apache.comet.parquet.Native.initRecordBatchReader(Native Method)
	at org.apache.comet.parquet.NativeBatchReader.init(NativeBatchReader.java:568)
	at org.apache.comet.parquet.CometParquetFileFormat.$anonfun$buildReaderWithPartitionValues$1(CometParquetFileFormat.scala:175)
	at org.apache.spark.sql.execution.datasources.FileScanRDD$$anon$1.org$apache$spark$sql$execution$datasources$FileScanRDD$$anon$$readCurrentFile(FileScanRDD.scala:217)
	at org.apache.spark.sql.execution.datasources.FileScanRDD$$anon$1.nextIterator(FileScanRDD.scala:279)
	at org.apache.spark.sql.execution.datasources.FileScanRDD$$anon$1.hasNext(FileScanRDD.scala:129)
	at org.apache.spark.sql.comet.CometScanExec$$anon$1.hasNext(CometScanExec.scala:273)
	at org.apache.comet.CometBatchIterator.hasNext(CometBatchIterator.java:60)
	at org.apache.comet.Native.executePlan(Native Method)
	at org.apache.comet.CometExecIterator.$anonfun$getNextBatch$2(CometExecIterator.scala:148)
	at org.apache.comet.CometExecIterator.$anonfun$getNextBatch$2$adapted(CometExecIterator.scala:147)
	at org.apache.comet.vector.NativeUtil.getNextBatch(NativeUtil.scala:212)
	at org.apache.comet.CometExecIterator.$anonfun$getNextBatch$1(CometExecIterator.scala:147)
	at org.apache.comet.Tracing$.withTrace(Tracing.scala:31)
	at org.apache.comet.CometExecIterator.getNextBatch(CometExecIterator.scala:145)
	at org.apache.comet.CometExecIterator.hasNext(CometExecIterator.scala:201)
	at scala.collection.Iterator$$anon$10.hasNext(Iterator.scala:460)
	at scala.collection.Iterator$$anon$10.hasNext(Iterator.scala:460)
	at org.apache.comet.CometBatchIterator.hasNext(CometBatchIterator.java:60)
	at org.apache.comet.Native.executePlan(Native Method)
	at org.apache.comet.CometExecIterator.$anonfun$getNextBatch$2(CometExecIterator.scala:148)
	at org.apache.comet.CometExecIterator.$anonfun$getNextBatch$2$adapted(CometExecIterator.scala:147)
	at org.apache.comet.vector.NativeUtil.getNextBatch(NativeUtil.scala:212)
	at org.apache.comet.CometExecIterator.$anonfun$getNextBatch$1(CometExecIterator.scala:147)
	at org.apache.comet.Tracing$.withTrace(Tracing.scala:31)
	at org.apache.comet.CometExecIterator.getNextBatch(CometExecIterator.scala:145)
	at org.apache.comet.CometExecIterator.hasNext(CometExecIterator.scala:201)
	at org.apache.spark.sql.comet.execution.shuffle.CometNativeShuffleWriter.write(CometNativeShuffleWriter.scala:110)
	at org.apache.spark.shuffle.ShuffleWriteProcessor.write(ShuffleWriteProcessor.scala:59)
	at org.apache.spark.scheduler.ShuffleMapTask.runTask(ShuffleMapTask.scala:104)
	at org.apache.spark.scheduler.ShuffleMapTask.runTask(ShuffleMapTask.scala:54)
	at org.apache.spark.TaskContext.runTaskWithListeners(TaskContext.scala:166)
	at org.apache.spark.scheduler.Task.run(Task.scala:141)
	at org.apache.spark.executor.Executor$TaskRunner.$anonfun$run$4(Executor.scala:620)
	at org.apache.spark.util.SparkErrorUtils.tryWithSafeFinally(SparkErrorUtils.scala:64)
	at org.apache.spark.util.SparkErrorUtils.tryWithSafeFinally$(SparkErrorUtils.scala:61)
	at org.apache.spark.util.Utils$.tryWithSafeFinally(Utils.scala:94)
	at org.apache.spark.executor.Executor$TaskRunner.run(Executor.scala:623)
	at java.base/java.util.concurrent.ThreadPoolExecutor.runWorker(ThreadPoolExecutor.java:1136)
	at java.base/java.util.concurrent.ThreadPoolExecutor$Worker.run(ThreadPoolExecutor.java:635)
	at java.base/java.lang.Thread.run(Thread.java:840)
```

#### Significantly Increased DNS query volume

Without Comet, DNS queries to the CoreDNS server stay around 5 to 10 queries per second during benchmarking.

With Comet, DNS queries can reach up to 5000 per second, which results in:

```
Received an UnknownHostException when attempting to interact with a service. See cause for the exact endpoint that is failing to resolve. If this is happening on an endpoint that previously worked, there may be a network connectivity issue or your DNS cache could be storing endpoints for too long.
```

This happens because of the 1024 packets per second per network interface limit in Route53 Resolver.

Comet may not be taking advantage of JVM DNS caching mechanisms, resulting in excessive DNS queries.

Use NodeLocal DNSCache to cache dns results locally: https://kubernetes.io/docs/tasks/administer-cluster/nodelocaldns/

#### Off heap requirements

With `spark.memory.offHeap.size` set to less than 16GB, the executors go OOM. Had to increase it to 32GB (total 58GB). Native Spark can do it with 32GB total memory.

