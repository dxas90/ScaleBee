# Quick Start Guide

## Prerequisites

- Docker Swarm initialized: `docker swarm init`
- You are on a manager node

## Step 1: Build and Deploy ScaleBee Stack

```bash
git clone https://github.com/dxas90/scalebee.git
cd scalebee

# Build ScaleBee image
docker build -t scalebee:latest .

# Deploy stack (ScaleBee + Prometheus)
docker stack deploy -c deploy/docker-compose.yml autoscale
```

This deploys:

- **ScaleBee**: Autoscaler with built-in metrics exporter
- **Prometheus**: Metrics storage and querying

## Step 2: Verify Deployment

```bash
# Check services
docker service ls

# Check ScaleBee logs
docker service logs -f autoscale_scalebee

# Check ScaleBee metrics endpoint
curl http://localhost:9090/metrics | head -20

# Query Prometheus for container metrics
curl 'http://localhost:9090/api/v1/query?query=container_cpu_usage_percent'
```

## Step 3: Deploy a Test Service

```bash
# Deploy the example service
docker stack deploy -c deploy/test-service.yml demo

# Verify it's running
docker service ls | grep scaletest_testapp
```

The example service has autoscaling configured:

- Min replicas: 2
- Max replicas: 10
- Autoscaling: enabled

## Step 4: Test Autoscaling

### Generate High CPU Load

```bash
# Scale manually to see initial state
docker service scale scaletest_testapp=2

# Get container IDs
docker ps | grep scaletest_testapp

# Enter a container and generate CPU load
docker exec -it <container_id> sh -c "yes > /dev/null &"
```

Wait 60 seconds (default interval) and watch ScaleBee scale up:

```bash
docker service logs -f autoscale_scalebee
# You should see: "Service scaletest_testapp is above 85% CPU usage"
# Then: "Scaling up service scaletest_testapp to 3"

docker service ps scaletest_testapp
```

### Stop Load and Scale Down

```bash
# Kill the load process
docker exec -it <container_id> killall yes

# Wait 60 seconds and watch scale down
docker service logs -f autoscale_scalebee
# You should see: "Service scaletest_testapp is below 25% CPU usage"
# Then: "Scaling down service scaletest_testapp to 2"
```

## Step 5: Monitor Your Services

Access Prometheus UI:

```bash
open http://localhost:9090
```

Query examples:

```text
# CPU usage by service
sum(rate(container_cpu_usage_seconds_total{container_label_com_docker_swarm_task_name=~'.+'}[5m]))BY(container_label_com_docker_swarm_service_name)*100

# Service replica count
count(container_last_seen{container_label_com_docker_swarm_service_name!=""}) by (container_label_com_docker_swarm_service_name)
```

## Adding Your Own Services

Add these labels to any service you want to autoscale:

```yaml
services:
  myservice:
    image: your-image:latest
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

Deploy:

```shell
docker stack deploy -c your-service.yml myapp
```

## Customizing ScaleBee

Edit `deploy/docker-compose.yml` to change autoscaler behavior:

```yaml
scalebee:
  environment:
    - PROMETHEUS_URL=http://prometheus:9090
    - INTERVAL_SECONDS=30              # Check every 30 seconds
    - CPU_PERCENTAGE_UPPER_LIMIT=90   # Scale up at 90%
    - CPU_PERCENTAGE_LOWER_LIMIT=20   # Scale down at 20%
```

Redeploy:

```bash
docker stack deploy -c deploy/docker-compose.yml autoscale
```

## Troubleshooting

### ScaleBee not starting

```bash
docker service ps autoscale_scalebee
docker service logs autoscale_scalebee
```

### Services not being autoscaled

1. Check service has correct labels:

   ```bash
   docker service inspect myservice --format '{{.Spec.Labels}}'
   ```

2. Check Prometheus has metrics from ScaleBee:

   ```bash
   curl 'http://localhost:9090/api/v1/query?query=container_cpu_usage_percent'
   ```

3. Check ScaleBee can access Docker socket:

   ```bash
   docker service logs autoscale_scalebee | grep "permission denied"
   ```

### ScaleBee metrics not appearing

1. Verify ScaleBee metrics endpoint:

   ```bash
   docker exec $(docker ps -q -f name=autoscale_scalebee) wget -qO- http://localhost:9090/metrics
   ```

2. Verify Prometheus scrape config:

   ```bash
   docker config inspect autoscale_prometheus_config --pretty
   ```

## Clean Up

Remove all stacks:

```bash
docker stack rm autoscale
docker stack rm demo
```

## Building Custom Image

If you modify the code:

```bash
# Build locally
go build -o scalebee .

# Build Docker image
docker build -t scalebee:latest .

# Or let docker-compose build it
docker stack deploy -c deploy/docker-compose.yml autoscale
```

## Next Steps

- Monitor your services for 24 hours to tune CPU thresholds
- Adjust min/max replicas based on your traffic patterns
- Set up alerts in Prometheus for scaling events
- Consider adding memory-based scaling (future feature)

## Getting Help

- Check logs: `docker service logs -f autoscale_scalebee`
- Review README.md for detailed documentation
- Check MIGRATION.md for architecture details
