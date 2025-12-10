# ScaleBee - AI Agent Instructions

## Project Overview
ScaleBee is a Docker Swarm autoscaler that collects container metrics directly from Docker and exposes them to Prometheus, enabling CPU and memory-based autoscaling decisions. Originally migrated from bash to Go.

## Architecture (3 Components)

1. **Metrics Exporter** (`pkg/metrics/exporter.go`)
   - Polls Docker socket every 10s for container stats via `ContainerStats()` API
   - Exposes Prometheus metrics on `:9090/metrics` with labels: `service`, `task`, `container_id`
   - Exports: `container_cpu_usage_percent`, `container_memory_usage_mb`, `container_memory_limit_mb`
   - Critical: Runs as root to access `/var/run/docker.sock` (Colima/dev requirement)

2. **Autoscaler Engine** (`pkg/autoscaler/autoscaler.go`)
   - Queries Prometheus for CPU: `avg(container_cpu_usage_percent) BY (service)`
   - Queries Prometheus for Memory: `(avg(container_memory_usage_mb) BY (service) / avg(container_memory_limit_mb) BY (service)) * 100`
   - Scale-up: CPU > 85% OR Memory > 85% (either threshold triggers scale-up)
   - Scale-down: CPU < 25% AND Memory < 25% (both must be below threshold)
   - Configurable thresholds via env vars
   - Only affects services with label `swarm.autoscaler=true`

3. **Docker Service Manager** (`pkg/docker/service.go`)
   - Reads labels: `swarm.autoscaler.{minimum,maximum}` for replica constraints
   - Updates service specs via Docker SDK `ServiceUpdate()` API

## Key Technical Decisions

### Docker Socket Permissions
**Problem**: Standard containers can't access Docker socket in Colima
**Solution**: Dockerfile runs as root (no `USER` directive). See `Dockerfile:L36-37` comment.
**Why**: Colima requires root access; production should use socket groups or user namespaces.

### No OTEL Collector
**Evolution**: Originally used OTEL → cAdvisor → Direct Docker stats
**Current**: ScaleBee collects metrics itself via `pkg/metrics/exporter.go`
**Reason**: Eliminates socket permission issues; simpler stack (just ScaleBee + Prometheus)

### Metric Format
- Export `container_cpu_usage_percent` (gauge) not `container_cpu_usage_seconds_total` (counter)
- Why: Simpler queries, no need for `rate()` calculations in autoscaler logic
- Prometheus query simplified from `sum(rate(...[5m]))` to `avg(container_cpu_usage_percent)`

## Development Workflow

### Build & Deploy
```bash
# Build image (9.6MB binary, multi-platform)
docker build -t scalebee:latest .

# Deploy stack (from deploy/ directory)
docker stack deploy -c docker-compose.yml autoscale

# Check logs
docker service logs autoscale_scalebee --tail 50
```

### Testing Autoscaling
1. Label a service: `swarm.autoscaler=true`, `swarm.autoscaler.{minimum,maximum}`
2. Generate load on the service
3. Watch logs: `Retrieved X service metrics` → `Scaling service X from Y to Z replicas`

### Common Issues
- **"permission denied" on socket**: Container not running as root (check Dockerfile USER directive)
- **"Retrieved 0 service metrics"**: Prometheus not scraping ScaleBee (check `prometheus.yml` targets)
- **Services not scaling**: Missing `swarm.autoscaler=true` label

## Code Patterns

### Environment Variables
All config via `getEnv()`, `getEnvInt()`, `getEnvFloat()` helpers in `main.go`:
```go
prometheusURL := getEnv("PROMETHEUS_URL", "http://prometheus:9090")
cpuLimit := getEnvFloat("CPU_PERCENTAGE_UPPER_LIMIT", 85.0)
memoryLimit := getEnvFloat("MEMORY_PERCENTAGE_UPPER_LIMIT", 85.0)
```

### Error Handling
- Wrap errors: `fmt.Errorf("context: %w", err)`
- Log and continue on non-fatal errors (metrics collection, scaling failures)
- Fatal on startup errors: `log.Fatalf("Failed to create client: %v", err)`

### Docker SDK Usage
- Always use `client.FromEnv` with `client.WithAPIVersionNegotiation()`
- Check Swarm labels: `container.Labels["com.docker.swarm.service.name"]`
- Non-blocking stats: `ContainerStats(ctx, id, false)` (stream=false)

### Prometheus Metrics Format
Follow OpenMetrics spec:
```
# HELP metric_name Description
# TYPE metric_name gauge|counter
metric_name{label="value"} 123.45
```

## File Structure
```
pkg/
├── autoscaler/   # Core scaling logic (Run loop, scale decisions)
├── docker/       # Swarm service inspection & updates
├── metrics/      # Docker stats → Prometheus exporter
└── prometheus/   # Query client for avg CPU per service
main.go           # Entry point, HTTP server, signal handling
Dockerfile        # Multi-stage build, runs as root
deploy/
├── docker-compose.yml  # Stack: ScaleBee + Prometheus only
└── prometheus.yml      # Scrapes scalebee:9090
```

## Reference Implementation

- Migration notes: `MIGRATION.md`
- Inspired by: github.com/jcwimer/docker-swarm-autoscaler
