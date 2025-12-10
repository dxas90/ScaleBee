# ScaleBee - Docker Swarm Autoscaler

ScaleBee is a Go-based autoscaler for Docker Swarm services that uses Prometheus metrics to automatically scale services up or down based on CPU usage.

## Features

- 🐝 Automatic scaling of Docker Swarm services based on CPU metrics
- 📊 Integration with Prometheus for real-time metrics
- ⚙️ Configurable CPU thresholds for scaling decisions
- 🔒 Respects minimum and maximum replica constraints
- 🔄 Continuous monitoring with configurable intervals
- 🐳 Runs as a Docker Swarm service

## How It Works

ScaleBee collects container metrics directly from Docker and exposes them to Prometheus, then uses those metrics to automatically adjust replica counts:

1. **Collect metrics** from Docker socket every 10 seconds (CPU, memory)
2. **Expose metrics** on `:9090/metrics` endpoint in Prometheus format
3. **Query Prometheus** for service CPU metrics every 60 seconds (configurable)
4. **Scale up** when CPU exceeds upper threshold (default: 85%)
5. **Scale down** when CPU falls below lower threshold (default: 25%)
6. **Respect limits** defined by min/max replica labels

## Architecture

```text
┌──────────────────────────────────────────────────────┐
│                    ScaleBee                          │
│  ┌────────────────┐         ┌──────────────────┐     │
│  │ Metrics        │────────▶│   Autoscaler     │     │
│  │ Exporter       │         │   Engine         │     │
│  │ :9090/metrics  │         │                  │     │
│  └────────┬───────┘         └────────┬─────────┘     │
│           │                          │               │
└───────────┼──────────────────────────┼────────────-──┘
            │                          │
            │                          │ Docker API
            │ Scrape                   │ (scale services)
            ▼                          ▼
    ┌──────────────┐         ┌─────────────────────┐
    │  Prometheus  │         │  Docker Swarm       │
    │              │         │  Services           │
    └──────────────┘         │  ┌────┐ ┌────┐      │
                             │  │ S1 │ │ S2 │ ...  │
                             │  └────┘ └────┘      │
                             └─────────────────────┘
```

## Usage

### Prerequisites

- Docker Swarm cluster initialized
- Docker socket access for ScaleBee container

### Quick Start

1. **Deploy the monitoring stack** (Prometheus + OpenTelemetry Collector):

   ```bash
   docker stack deploy -c deploy/docker-compose.yml autoscale
   ```

2. **Label your services** for autoscaling:

   ```yaml
   version: "3.7"
   services:
     myapp:
       image: myapp:latest
       deploy:
         labels:
           - "swarm.autoscaler=true"
           - "swarm.autoscaler.minimum=2"
           - "swarm.autoscaler.maximum=10"
         replicas: 2
         resources:
           limits:
             cpus: '0.5'
             memory: 512M
   ```

3. **Deploy your application**:

   ```bash
   docker stack deploy -c your-app.yml myapp
   ```

### Configuration

ScaleBee is configured through environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `PROMETHEUS_URL` | `http://prometheus:9090` | URL of the Prometheus server |
| `LOOP` | `yes` | Enable continuous monitoring (`yes` or `no`) |
| `INTERVAL_SECONDS` | `15` | Seconds between autoscaling checks |
| `CPU_PERCENTAGE_UPPER_LIMIT` | `75` | CPU % threshold for scaling up |
| `CPU_PERCENTAGE_LOWER_LIMIT` | `20` | CPU % threshold for scaling down |
| `MEMORY_PERCENTAGE_UPPER_LIMIT` | `80` | Memory % threshold for scaling up |
| `MEMORY_PERCENTAGE_LOWER_LIMIT` | `20` | Memory % threshold for scaling down |
| `METRICS_ENABLED` | `yes` | Enable built-in metrics exporter |
| `METRICS_PORT` | `9090` | Port for metrics HTTP server |

**Scaling Logic:**

- **Scale up** when **either** CPU **or** Memory exceeds their upper limits
- **Scale down** when **both** CPU **and** Memory are below their lower limits

### Service Labels

Services must have the following labels to enable autoscaling:

| Label | Required | Description |
|-------|----------|-------------|
| `swarm.autoscaler` | ✅ Yes | Set to `"true"` to enable autoscaling |
| `swarm.autoscaler.minimum` | ⚠️ Recommended | Minimum number of replicas (e.g., `"2"`) |
| `swarm.autoscaler.maximum` | ⚠️ Recommended | Maximum number of replicas (e.g., `"10"`) |

## Example Deployment

See the `deploy/docker-compose.yml` for a complete example including:

- ScaleBee (autoscaler + metrics exporter)
- Prometheus for metrics storage and querying

### Metrics Endpoint

ScaleBee exposes Prometheus metrics at `http://scalebee:9090/metrics`:

```prometheus
# HELP container_cpu_usage_percent CPU usage percentage
# TYPE container_cpu_usage_percent gauge
container_cpu_usage_percent{service="myapp",task="myapp.1.xyz",container_id="abc123"} 45.2

# HELP container_memory_usage_mb Memory usage in MB
# TYPE container_memory_usage_mb gauge
container_memory_usage_mb{service="myapp",task="myapp.1.xyz",container_id="abc123"} 128.5
```

## Building from Source

```bash
# Build binary
go build -o scalebee .

# Build Docker image
docker build -t scalebee:latest .

# Run locally (requires Docker socket access)
export PROMETHEUS_URL=http://localhost:9090
./scalebee
```

## Development

### Project Structure

```text
.
├── main.go                    # Entry point with HTTP server
├── pkg/
│   ├── autoscaler/           # Autoscaling logic
│   │   └── autoscaler.go
│   ├── docker/               # Docker Swarm service management
│   │   └── service.go
│   ├── metrics/              # Docker stats → Prometheus exporter
│   │   └── exporter.go
│   └── prometheus/           # Prometheus query client
│       └── client.go
├── deploy/                    # Deployment configurations
│   ├── docker-compose.yml    # Stack: ScaleBee + Prometheus
│   ├── prometheus.yml        # Prometheus config
│   └── example-service.yml   # Example autoscaled service
├── Dockerfile                 # Multi-stage build
└── README.md
```

### Running Tests

```bash
go test ./...
```

## Scaling Behavior

### Scale Up

- Triggered when average CPU > `CPU_PERCENTAGE_UPPER_LIMIT` (default 85%)
- Increases replicas by 1
- Will not exceed `swarm.autoscaler.maximum` label

### Scale Down

- Triggered when average CPU < `CPU_PERCENTAGE_LOWER_LIMIT` (default 25%)
- Decreases replicas by 1
- Will not go below `swarm.autoscaler.minimum` label

### Default Scaling

- On each check, ensures replicas are within min/max bounds
- Useful for services that drift from their configured limits

## Monitoring

ScaleBee logs all scaling decisions:

```shell
2024/12/10 13:53:00 ScaleBee - Docker Swarm Autoscaler
2024/12/10 13:53:00 Prometheus URL: http://prometheus:9090
2024/12/10 13:53:00 CPU Upper Limit: 85%
2024/12/10 13:53:00 CPU Lower Limit: 25%
2024/12/10 13:53:05 Retrieved 3 service metrics from Prometheus
2024/12/10 13:53:05 Service: myapp_web, Avg CPU: 92.34%
2024/12/10 13:53:05 Service myapp_web has autoscale label
2024/12/10 13:53:05 Service myapp_web is above 85% CPU usage
2024/12/10 13:53:05 Scaling up service myapp_web to 4
```

## Migration from Shell Script

This is a complete rewrite of the original bash-based autoscaler in Go. Key improvements:

- ✅ Better error handling and logging
- ✅ Type safety and compile-time checks
- ✅ Easier testing and maintenance
- ✅ Native Docker SDK integration
- ✅ Structured configuration
- ✅ No dependencies on external tools (jq, curl, etc.)

## Based On

This project is inspired by [jcwimer/docker-swarm-autoscaler](https://github.com/jcwimer/docker-swarm-autoscaler) and reimplemented using the [Docker Compose SDK](https://docs.docker.com/compose/compose-sdk/).

## License

MIT License - See LICENSE file for details

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.
