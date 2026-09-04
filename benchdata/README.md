# Benchmark data

Downloaded data in this directory is not tracked by Git.

## OpenTelemetry Demo

This downloads and extracts the demo source. It does not capture trace data.

```sh
mkdir -p benchdata/opentelemetry-demo
curl -L --fail --retry 5 --continue-at - \
  --output benchdata/opentelemetry-demo-main.tar.gz \
  https://github.com/open-telemetry/opentelemetry-demo/archive/refs/heads/main.tar.gz
tar -xzf benchdata/opentelemetry-demo-main.tar.gz \
  --strip-components=1 \
  -C benchdata/opentelemetry-demo
```

## Alibaba microservice call graphs

The complete set contains 145 compressed shards numbered 0 through 144. The
downloaded archives total 26,454,958,866 bytes.

```sh
mkdir -p benchdata/alibaba-ms-traces-2021/MSCallGraph
seq 0 144 | xargs -P 8 -I {} curl --silent --show-error \
  -L --fail --retry 8 --retry-delay 3 --continue-at - \
  --output benchdata/alibaba-ms-traces-2021/MSCallGraph/MSCallGraph_{}.tar.gz \
  https://aliopentrace.oss-cn-beijing.aliyuncs.com/v2021MicroservicesTraces/MSCallGraph/MSCallGraph_{}.tar.gz
```

Validate the downloaded archives without extracting them:

```sh
find benchdata/alibaba-ms-traces-2021/MSCallGraph \
  -maxdepth 1 -name 'MSCallGraph_*.tar.gz' -type f -print0 \
  | xargs -0 -P 8 -n 1 gzip -t
```
