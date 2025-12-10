# ScaleBee Deployment

This directory contains the deployment configuration for ScaleBee autoscaler.

## Stack Components

- **ScaleBee**: Autoscaler with built-in metrics exporter
- **Prometheus**: Metrics storage and querying

## Quick Deploy

```bash
# Build ScaleBee image
docker build -t scalebee:latest ..

# Deploy stack
docker stack deploy -c docker-compose.yml autoscale
```

## Verify Deployment

```bash
# Check services
docker service ls

# Check ScaleBee logs
docker service logs -f autoscale_scalebee

# Check metrics endpoint
curl http://localhost:9090/metrics

# Query Prometheus
curl 'http://localhost:9090/api/v1/query?query=container_cpu_usage_percent'
```

## Configuration Files

- `docker-compose.yml`: Main stack definition
- `prometheus.yml`: Prometheus scrape configuration
- `alert-rules.yml`: Prometheus alerting rules
- `example-service.yml`: Sample autoscaled service

## Autoscaling Your Services

Label your services with:

```yaml
deploy:
  labels:
    - "swarm.autoscaler=true"
    - "swarm.autoscaler.minimum=2"
    - "swarm.autoscaler.maximum=10"
```
