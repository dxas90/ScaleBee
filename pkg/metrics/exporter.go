package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// Exporter collects Docker container stats and exposes them as Prometheus metrics
type Exporter struct {
	dockerClient *client.Client
	mu           sync.RWMutex
	metrics      map[string]*ContainerMetrics
	prevStats    map[string]*container.StatsResponse
	interval     time.Duration
}

// ContainerMetrics holds CPU and memory metrics for a container
type ContainerMetrics struct {
	ServiceName   string
	TaskName      string
	ContainerID   string
	CPUPercentage float64
	MemoryUsageMB float64
	MemoryLimitMB float64
	LastUpdate    time.Time
}

// NewExporter creates a new metrics exporter
func NewExporter(interval time.Duration) (*Exporter, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &Exporter{
		dockerClient: cli,
		metrics:      make(map[string]*ContainerMetrics),
		prevStats:    make(map[string]*container.StatsResponse),
		interval:     interval,
	}, nil
}

// Start begins collecting metrics in the background
func (e *Exporter) Start(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	// Collect immediately on start
	if err := e.collectMetrics(ctx); err != nil {
		log.Printf("Error collecting initial metrics: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.collectMetrics(ctx); err != nil {
				log.Printf("Error collecting metrics: %v", err)
			}
		}
	}
}

// collectMetrics gets stats from all running containers
func (e *Exporter) collectMetrics(ctx context.Context) error {
	// List all containers (including Swarm tasks)
	containerFilters := filters.NewArgs()
	containerFilters.Add("status", "running")

	containers, err := e.dockerClient.ContainerList(ctx, container.ListOptions{
		Filters: containerFilters,
	})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	newMetrics := make(map[string]*ContainerMetrics)

	for _, ctr := range containers {
		// Get container stats
		stats, err := e.getContainerStats(ctx, ctr.ID)
		if err != nil {
			log.Printf("Failed to get stats for container %s: %v", ctr.ID[:12], err)
			continue
		}

		// Extract service and task names from labels
		serviceName := ctr.Labels["com.docker.swarm.service.name"]
		taskName := ctr.Labels["com.docker.swarm.task.name"]

		// Skip containers without Swarm labels
		if serviceName == "" {
			continue
		}

		containerMetrics := &ContainerMetrics{
			ServiceName:   serviceName,
			TaskName:      taskName,
			ContainerID:   ctr.ID[:12],
			CPUPercentage: stats.CPUPercentage,
			MemoryUsageMB: stats.MemoryUsageMB,
			MemoryLimitMB: stats.MemoryLimitMB,
			LastUpdate:    time.Now(),
		}

		newMetrics[ctr.ID] = containerMetrics
	}

	e.mu.Lock()
	e.metrics = newMetrics
	e.mu.Unlock()

	return nil
}

// ContainerStats holds calculated stats
type ContainerStats struct {
	CPUPercentage float64
	MemoryUsageMB float64
	MemoryLimitMB float64
}

// getContainerStats retrieves and calculates stats for a container
func (e *Exporter) getContainerStats(ctx context.Context, containerID string) (*ContainerStats, error) {
	stats, err := e.dockerClient.ContainerStats(ctx, containerID, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get container stats: %w", err)
	}
	defer stats.Body.Close()

	var v container.StatsResponse
	if err := json.NewDecoder(stats.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("failed to decode stats: %w", err)
	}

	// Calculate CPU percentage using previous stats if available
	var cpuPercent float64
	if prevStat, exists := e.prevStats[containerID]; exists {
		cpuPercent = calculateCPUPercentWithPrevious(&v, prevStat)
	} else {
		// First time seeing this container, use PreCPUStats
		cpuPercent = calculateCPUPercent(&v)
	}

	// Store current stats for next iteration
	e.prevStats[containerID] = &v

	// Calculate memory usage
	memUsageMB := float64(v.MemoryStats.Usage) / 1024 / 1024
	memLimitMB := float64(v.MemoryStats.Limit) / 1024 / 1024

	return &ContainerStats{
		CPUPercentage: cpuPercent,
		MemoryUsageMB: memUsageMB,
		MemoryLimitMB: memLimitMB,
	}, nil
}

// calculateCPUPercentWithPrevious calculates CPU percentage using stored previous stats
func calculateCPUPercentWithPrevious(current, previous *container.StatsResponse) float64 {
	cpuDelta := float64(current.CPUStats.CPUUsage.TotalUsage - previous.CPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(current.CPUStats.SystemUsage - previous.CPUStats.SystemUsage)

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		numCPUs := float64(len(current.CPUStats.CPUUsage.PercpuUsage))
		if numCPUs == 0 {
			numCPUs = 1.0
		}
		return (cpuDelta / systemDelta) * numCPUs * 100.0
	}
	return 0.0
}

// calculateCPUPercent calculates CPU percentage from stats
func calculateCPUPercent(stats *container.StatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		return (cpuDelta / systemDelta) * float64(len(stats.CPUStats.CPUUsage.PercpuUsage)) * 100.0
	}
	return 0.0
}

// ServeHTTP implements http.Handler for Prometheus metrics endpoint
func (e *Exporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var sb strings.Builder

	// Write metric help and type
	sb.WriteString("# HELP container_cpu_usage_percent CPU usage percentage of the container\n")
	sb.WriteString("# TYPE container_cpu_usage_percent gauge\n")

	for _, m := range e.metrics {
		sb.WriteString(fmt.Sprintf(
			`container_cpu_usage_percent{service="%s",task="%s",container_id="%s"} %.2f`+"\n",
			m.ServiceName, m.TaskName, m.ContainerID, m.CPUPercentage,
		))
	}

	sb.WriteString("\n")
	sb.WriteString("# HELP container_memory_usage_mb Memory usage in megabytes\n")
	sb.WriteString("# TYPE container_memory_usage_mb gauge\n")

	for _, m := range e.metrics {
		sb.WriteString(fmt.Sprintf(
			`container_memory_usage_mb{service="%s",task="%s",container_id="%s"} %.2f`+"\n",
			m.ServiceName, m.TaskName, m.ContainerID, m.MemoryUsageMB,
		))
	}

	sb.WriteString("\n")
	sb.WriteString("# HELP container_memory_limit_mb Memory limit in megabytes\n")
	sb.WriteString("# TYPE container_memory_limit_mb gauge\n")

	for _, m := range e.metrics {
		sb.WriteString(fmt.Sprintf(
			`container_memory_limit_mb{service="%s",task="%s",container_id="%s"} %.2f`+"\n",
			m.ServiceName, m.TaskName, m.ContainerID, m.MemoryLimitMB,
		))
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	io.WriteString(w, sb.String())
}

// Close closes the Docker client
func (e *Exporter) Close() error {
	if e.dockerClient != nil {
		return e.dockerClient.Close()
	}
	return nil
}
