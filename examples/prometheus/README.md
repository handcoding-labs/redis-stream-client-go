# Prometheus Metrics Recorder Example

This folder contains a sample implementation of the `metrics.Recorder` interface that
exports counters and histograms to Prometheus.  You can copy `recorder.go` into your own
codebase or import it directly if you prefer.

## Usage

1. Install Prometheus client:

    ```bash
    go get github.com/prometheus/client_golang/prometheus
    go get github.com/prometheus/client_golang/prometheus/promhttp
    ```

2. Create a recorder and register it:

    ```go
    import (
        "net/http"
        prom "github.com/handcoding-labs/redis-stream-client-go/examples/prometheus"
        "github.com/prometheus/client_golang/prometheus"
        "github.com/prometheus/client_golang/prometheus/promhttp"
    )

    func main() {
        rec := prom.NewPrometheusRecorder(prometheus.DefaultRegisterer)
        client, _ := impl.NewRedisStreamClient(redisClient, "my-service", impl.WithMetricsRecorder(rec))

        // start HTTP endpoint for scraping
        http.Handle("/metrics", promhttp.Handler())
        go http.ListenAndServe(":2112", nil)

        // ...rest of application...
    }
    ```

3. Scrape the `/metrics` endpoint with your Prometheus server.  See [`docs/METRICS.md`](../../docs/METRICS.md)
for a complete description of each metric and suggested alerting queries.

## Notes

- The example tracks stream processing duration by recording the start time and computing
the elapsed time in `RecordStreamProcessingEnd`.
- When modifying the interface in the future, update this file to match the new method
signatures.
