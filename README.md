# redis-stream-client
A redis stream based client that can recover from failures.

Redis streams are awesome! Typically they are used for data written in one end and consumed at other.

![Redis streams normal working](./imgs/redis_stream_normal.png)

When one (or more) of the consumers fail (crash, get stuck for abnormal period of time), the way to recover is by using XCLAIM (and XAUTOCLAIM) per redis streams. This supposes that your consumers are stateful i.e. they know who they are via a dedicated machine name or IP. This way using XPENDING and XCLAIM you can recover from failed or stuck situations.

![Redis streams failure recovery](./imgs/redis_stream_failure_recovery.png)

This is good but there are two requirements that it doesn't meet:
1. Recovery depends on how soon the crashed consumer can come back up and claim. This is normally a small time (few seconds) but sometimes it can be high due to startup logic.
2. When a consumer gets stuck (GC or some such stop-the-world process) then the processing is stuck.

In both situations above, there are other consumers waiting and perhaps availble who can claim and continue processing in real-time. However, due to redis' pull based mechanism they don't know if they need to.

This library aims to provide two such constructs built on top of redis' own data structures:
1. Inform other consumers that a consumer is dead or stuck via key space notifications.
2. Provide API to claim the stream being processed.

![Redis streams failure recovery - new](./imgs/redis_stream_failure_recovery-redis-stream-client_way.png)

In addition to this, for better management, the library provides a load balancer stream (LBS) based on redis streams and consumer groups that work in a load balanced fashion which can distribute incoming streams (not stream data!) among existing consumers using round-robin fashion.

![Redis stream client - LBS](./imgs/redis_stream_client_lbs.png)