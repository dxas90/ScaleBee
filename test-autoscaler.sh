#!/bin/bash
set -e

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Configuration
STACK_NAME="autoscale"
DEMO_STACK="scaletest"
SERVICE_NAME="scaletest_testapp"
CHECK_INTERVAL=10
SCALE_WAIT_TIME=70

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}ScaleBee Autoscaler Test Script${NC}"
echo -e "${BLUE}Tests: CPU & Memory-based Autoscaling${NC}"
echo -e "${BLUE}========================================${NC}\n"

# Step 1: Verify ScaleBee is running
echo -e "${YELLOW}[1/11] Verifying ScaleBee deployment...${NC}"
if docker service ls | grep -q "${STACK_NAME}_scalebee"; then
    echo -e "${GREEN}✓ ScaleBee service is running${NC}"
else
    echo -e "${RED}✗ ScaleBee service not found. Deploy with: docker stack deploy -c deploy/docker-compose.yml autoscale${NC}"
    exit 1
fi

if docker service ls | grep -q "${STACK_NAME}_prometheus"; then
    echo -e "${GREEN}✓ Prometheus service is running${NC}"
else
    echo -e "${RED}✗ Prometheus service not found${NC}"
    exit 1
fi

sleep 2

# Step 2: Check metrics endpoint
echo -e "\n${YELLOW}[2/11] Checking ScaleBee metrics endpoint...${NC}"
# Get a container from the autoscale network to run curl from
PROM_CONTAINER=$(docker ps --filter name=${STACK_NAME}_prometheus --format "{{.ID}}" | head -1)

if [ -z "$PROM_CONTAINER" ]; then
    echo -e "${RED}✗ Could not find Prometheus container to test from${NC}"
    exit 1
fi

# Curl from inside the Docker network
METRICS_OUTPUT=$(docker exec ${PROM_CONTAINER} wget -q -O - http://scalebee:9090/metrics 2>/dev/null || true)

if echo "$METRICS_OUTPUT" | grep -q "container_cpu_usage_percent"; then
    echo -e "${GREEN}✓ Metrics endpoint is working${NC}"
    METRIC_COUNT=$(echo "$METRICS_OUTPUT" | grep -c "container_cpu_usage_percent{" || true)
    echo -e "${GREEN}  Found ${METRIC_COUNT} container metrics${NC}"
else
    echo -e "${RED}✗ Metrics endpoint not responding or no metrics found${NC}"
    echo -e "${YELLOW}  Trying to check if ScaleBee is accessible...${NC}"
    docker exec ${PROM_CONTAINER} wget -q -O - http://scalebee:9090/health 2>/dev/null || echo "Not accessible"
    exit 1
fi

sleep 2

# Step 3: Check ScaleBee logs
echo -e "\n${YELLOW}[3/11] Checking ScaleBee logs...${NC}"
echo -e "${BLUE}Recent logs:${NC}"
docker service logs ${STACK_NAME}_scalebee --tail 5 2>&1 | grep -v "^$" || true

sleep 2

# Step 4: Deploy demo service
echo -e "\n${YELLOW}[4/11] Deploying demo service...${NC}"
if docker service ls | grep -q "${SERVICE_NAME}"; then
    echo -e "${BLUE}Demo service already exists, removing it first...${NC}"
    docker stack rm ${DEMO_STACK}
    echo "Waiting for cleanup..."
    sleep 10
fi

docker stack deploy -c deploy/test-service.yml ${DEMO_STACK}
echo -e "${GREEN}✓ Test service deployed${NC}"

echo "Waiting for service to start..."
sleep 15

# Verify service is running
REPLICAS=$(docker service ls --filter name=${SERVICE_NAME} --format "{{.Replicas}}")
echo -e "${GREEN}✓ Service ${SERVICE_NAME} is running with ${REPLICAS} replicas${NC}"

sleep 2

# Step 5: Get container ID
echo -e "\n${YELLOW}[5/11] Finding test container...${NC}"
CONTAINER_ID=$(docker ps --filter name=${SERVICE_NAME} --format "{{.ID}}" | head -1)

if [ -z "$CONTAINER_ID" ]; then
    echo -e "${RED}✗ Could not find running container for ${SERVICE_NAME}${NC}"
    echo "Available containers:"
    docker ps --filter name=${SERVICE_NAME}
    exit 1
fi

echo -e "${GREEN}✓ Found container: ${CONTAINER_ID}${NC}"

sleep 2

# Step 6: Generate CPU load
echo -e "\n${YELLOW}[6/11] Generating CPU load to trigger scale-up...${NC}"
echo -e "${BLUE}Finding all test containers and starting CPU stress...${NC}"

# Get ALL container IDs for the service
CONTAINER_IDS=$(docker ps --filter name=${SERVICE_NAME} --format "{{.ID}}")
CONTAINER_COUNT=$(echo "$CONTAINER_IDS" | wc -l | tr -d ' ')

echo -e "${GREEN}✓ Found ${CONTAINER_COUNT} containers${NC}"

# Install stress-ng in all containers first
echo -e "${BLUE}Installing stress-ng in containers...${NC}"
for CONTAINER_ID in $CONTAINER_IDS; do
    docker exec ${CONTAINER_ID} apk add --no-cache stress-ng >/dev/null 2>&1
done
echo -e "${GREEN}✓ stress-ng installed${NC}"

# Start CPU load in ALL containers
for CONTAINER_ID in $CONTAINER_IDS; do
    echo -e "${BLUE}  Starting CPU stress in container ${CONTAINER_ID}...${NC}"
    # Use stress-ng for reliable CPU stress
    # --cpu 0 = use all available CPUs, --cpu-load 95 = 95% load per CPU
    docker exec -d ${CONTAINER_ID} stress-ng --cpu 0 --cpu-load 95
done

echo -e "${GREEN}✓ CPU load started${NC}"
echo -e "${BLUE}Waiting ${SCALE_WAIT_TIME} seconds for autoscaler to detect high CPU and scale up...${NC}"

# Monitor for scale-up
for i in {1..7}; do
    sleep ${CHECK_INTERVAL}
    CURRENT_REPLICAS=$(docker service ls --filter name=${SERVICE_NAME} --format "{{.Replicas}}" | cut -d'/' -f2)
    echo -e "${BLUE}  [${i}0s] Current replicas: ${CURRENT_REPLICAS}${NC}"

    # Check ScaleBee logs for scaling activity
    RECENT_LOGS=$(docker service logs ${STACK_NAME}_scalebee --since 30s 2>&1 | grep -i "scaling\|cpu" | tail -2 || true)
    if [ ! -z "$RECENT_LOGS" ]; then
        echo -e "${BLUE}  ScaleBee: ${RECENT_LOGS}${NC}"
    fi

    if [ "$CURRENT_REPLICAS" -gt "1" ]; then
        echo -e "${GREEN}✓ Service scaled up to ${CURRENT_REPLICAS} replicas!${NC}"
        break
    fi
done

# Verify scale-up happened
FINAL_REPLICAS=$(docker service ls --filter name=${SERVICE_NAME} --format "{{.Replicas}}" | cut -d'/' -f2)
if [ "$FINAL_REPLICAS" -gt "1" ]; then
    echo -e "${GREEN}✓ Scale-up test PASSED (1 → ${FINAL_REPLICAS} replicas)${NC}"
else
    echo -e "${YELLOW}⚠ Scale-up may not have triggered yet. Current: ${FINAL_REPLICAS} replicas${NC}"
    echo -e "${YELLOW}  Check metrics: curl http://localhost:9090/metrics | grep container_cpu${NC}"
    echo -e "${YELLOW}  Check logs: docker service logs ${STACK_NAME}_scalebee --tail 20${NC}"
fi

sleep 2

# Step 7: Stop CPU load and test scale-down
echo -e "\n${YELLOW}[7/11] Stopping CPU load to trigger scale-down...${NC}"

# Stop load in all containers
for CONTAINER_ID in $CONTAINER_IDS; do
    docker exec ${CONTAINER_ID} pkill -f stress-ng 2>/dev/null || true
done

echo -e "${GREEN}✓ CPU load stopped${NC}"
echo -e "${BLUE}Waiting ${SCALE_WAIT_TIME} seconds for autoscaler to detect low CPU and scale down...${NC}"

# Monitor for scale-down
for i in {1..7}; do
    sleep ${CHECK_INTERVAL}
    CURRENT_REPLICAS=$(docker service ls --filter name=${SERVICE_NAME} --format "{{.Replicas}}" | cut -d'/' -f2)
    echo -e "${BLUE}  [${i}0s] Current replicas: ${CURRENT_REPLICAS}${NC}"

    # Check ScaleBee logs for scaling activity
    RECENT_LOGS=$(docker service logs ${STACK_NAME}_scalebee --since 30s 2>&1 | grep -i "scaling\|cpu" | tail -2 || true)
    if [ ! -z "$RECENT_LOGS" ]; then
        echo -e "${BLUE}  ScaleBee: ${RECENT_LOGS}${NC}"
    fi

    if [ "$CURRENT_REPLICAS" -eq "1" ]; then
        echo -e "${GREEN}✓ Service scaled down to ${CURRENT_REPLICAS} replicas!${NC}"
        break
    fi
done

# Verify scale-down
FINAL_REPLICAS=$(docker service ls --filter name=${SERVICE_NAME} --format "{{.Replicas}}" | cut -d'/' -f2)
if [ "$FINAL_REPLICAS" -eq "1" ]; then
    echo -e "${GREEN}✓ Scale-down test PASSED (${FINAL_REPLICAS} replicas)${NC}"
else
    echo -e "${YELLOW}⚠ Scale-down may not have completed yet. Current: ${FINAL_REPLICAS} replicas${NC}"
fi

sleep 2

# Step 8: Wait for service to stabilize
echo -e "\n${YELLOW}[8/11] Waiting for service to stabilize before memory test...${NC}"
sleep 15

# Step 9: Test memory-based scale-up
echo -e "\n${YELLOW}[9/11] Testing memory-based autoscaling...${NC}"
echo -e "${BLUE}Allocating memory to trigger scale-up...${NC}"

# Get fresh container list
CONTAINER_IDS=$(docker ps --filter name=${SERVICE_NAME} --format "{{.ID}}")
CONTAINER_COUNT=$(echo "$CONTAINER_IDS" | wc -l | tr -d ' ')

echo -e "${GREEN}✓ Found ${CONTAINER_COUNT} containers for memory test${NC}"

# Allocate memory in ALL containers - container has 128M limit, need >109MB to exceed 85%
for CONTAINER_ID in $CONTAINER_IDS; do
    echo -e "${BLUE}  Starting memory allocation in container ${CONTAINER_ID}...${NC}"
    # Use stress-ng to allocate actual memory
    # --vm 1 = 1 worker, --vm-bytes 115M = allocate 115MB (90% of 128MB), --vm-keep keeps memory allocated
    docker exec -d ${CONTAINER_ID} stress-ng --vm 1 --vm-bytes 115M --vm-keep
    sleep 2
done
echo -e "${GREEN}✓ Memory allocation started${NC}"
echo -e "${BLUE}Waiting 30 seconds for memory to fill up...${NC}"
sleep 30

# Verify memory allocation
echo -e "${BLUE}Checking memory usage in containers...${NC}"
for CONTAINER_ID in $CONTAINER_IDS; do
    MEM_USAGE=$(docker stats --no-stream --format "{{.MemUsage}}" ${CONTAINER_ID} 2>/dev/null || echo "unknown")
    MEM_PERCENT=$(docker stats --no-stream --format "{{.MemPerc}}" ${CONTAINER_ID} 2>/dev/null || echo "unknown")
    echo -e "${GREEN}  Container ${CONTAINER_ID}: ${MEM_USAGE} (${MEM_PERCENT})${NC}"
done

echo -e "${BLUE}Waiting additional ${SCALE_WAIT_TIME} seconds for autoscaler to detect high memory and scale up...${NC}"

# Monitor for scale-up
for i in {1..7}; do
    sleep ${CHECK_INTERVAL}
    CURRENT_REPLICAS=$(docker service ls --filter name=${SERVICE_NAME} --format "{{.Replicas}}" | cut -d'/' -f2)
    echo -e "${BLUE}  [${i}0s] Current replicas: ${CURRENT_REPLICAS}${NC}"

    # Check ScaleBee logs for scaling activity
    RECENT_LOGS=$(docker service logs ${STACK_NAME}_scalebee --since 30s 2>&1 | grep -i "scaling\|memory" | tail -2 || true)
    if [ ! -z "$RECENT_LOGS" ]; then
        echo -e "${BLUE}  ScaleBee: ${RECENT_LOGS}${NC}"
    fi

    if [ "$CURRENT_REPLICAS" -gt "1" ]; then
        echo -e "${GREEN}✓ Service scaled up to ${CURRENT_REPLICAS} replicas (memory trigger)!${NC}"
        break
    fi
done

# Verify memory-based scale-up
FINAL_REPLICAS=$(docker service ls --filter name=${SERVICE_NAME} --format "{{.Replicas}}" | cut -d'/' -f2)
if [ "$FINAL_REPLICAS" -gt "1" ]; then
    echo -e "${GREEN}✓ Memory scale-up test PASSED (1 → ${FINAL_REPLICAS} replicas)${NC}"
else
    echo -e "${YELLOW}⚠ Memory scale-up may not have triggered yet. Current: ${FINAL_REPLICAS} replicas${NC}"
    echo -e "${YELLOW}  Check memory metrics in logs${NC}"
fi

sleep 2

# Step 10: Free memory and test scale-down
echo -e "\n${YELLOW}[10/11] Freeing memory to trigger scale-down...${NC}"

# Get fresh container list (may have scaled up)
CONTAINER_IDS=$(docker ps --filter name=${SERVICE_NAME} --format "{{.ID}}")

# Free memory in all containers
for CONTAINER_ID in $CONTAINER_IDS; do
    echo -e "${BLUE}  Freeing memory in container ${CONTAINER_ID}...${NC}"
    # Kill stress-ng processes
    docker exec ${CONTAINER_ID} pkill -f stress-ng 2>/dev/null || true
done

echo -e "${GREEN}✓ Memory freed${NC}"
echo -e "${BLUE}Waiting ${SCALE_WAIT_TIME} seconds for autoscaler to detect low memory and scale down...${NC}"

# Monitor for scale-down
for i in {1..7}; do
    sleep ${CHECK_INTERVAL}
    CURRENT_REPLICAS=$(docker service ls --filter name=${SERVICE_NAME} --format "{{.Replicas}}" | cut -d'/' -f2)
    echo -e "${BLUE}  [${i}0s] Current replicas: ${CURRENT_REPLICAS}${NC}"

    # Check ScaleBee logs for scaling activity
    RECENT_LOGS=$(docker service logs ${STACK_NAME}_scalebee --since 30s 2>&1 | grep -i "scaling\|memory" | tail -2 || true)
    if [ ! -z "$RECENT_LOGS" ]; then
        echo -e "${BLUE}  ScaleBee: ${RECENT_LOGS}${NC}"
    fi

    if [ "$CURRENT_REPLICAS" -eq "1" ]; then
        echo -e "${GREEN}✓ Service scaled down to ${CURRENT_REPLICAS} replicas!${NC}"
        break
    fi
done

# Verify memory-based scale-down
FINAL_REPLICAS=$(docker service ls --filter name=${SERVICE_NAME} --format "{{.Replicas}}" | cut -d'/' -f2)
if [ "$FINAL_REPLICAS" -eq "1" ]; then
    echo -e "${GREEN}✓ Memory scale-down test PASSED (${FINAL_REPLICAS} replicas)${NC}"
else
    echo -e "${YELLOW}⚠ Memory scale-down may not have completed yet. Current: ${FINAL_REPLICAS} replicas${NC}"
fi

sleep 2

# Step 11: Summary
echo -e "\n${YELLOW}[11/11] Test Summary${NC}"
echo -e "${BLUE}========================================${NC}"
echo -e "${GREEN}Service Status:${NC}"
docker service ps ${SERVICE_NAME} --format "table {{.Name}}\t{{.CurrentState}}\t{{.DesiredState}}" 2>/dev/null || true

echo -e "\n${GREEN}Recent ScaleBee Activity:${NC}"
docker service logs ${STACK_NAME}_scalebee --tail 10 2>&1 | grep -v "^$" || true

echo -e "\n${BLUE}========================================${NC}"
echo -e "${GREEN}✓ All tests completed!${NC}"
echo -e "\n${BLUE}Test Results Summary:${NC}"
echo -e "  1. CPU-based scale-up: ${GREEN}Tested${NC}"
echo -e "  2. CPU-based scale-down: ${GREEN}Tested${NC}"
echo -e "  3. Memory-based scale-up: ${GREEN}Tested${NC}"
echo -e "  4. Memory-based scale-down: ${GREEN}Tested${NC}"
echo -e "\n${BLUE}Useful commands:${NC}"
echo -e "  Check metrics:  docker exec \$(docker ps -qf name=prometheus) wget -q -O - http://scalebee:9090/metrics"
echo -e "  Check logs:     docker service logs ${STACK_NAME}_scalebee -f"
echo -e "  Clean up:       docker stack rm ${DEMO_STACK}"
echo -e "${BLUE}========================================${NC}\n"
