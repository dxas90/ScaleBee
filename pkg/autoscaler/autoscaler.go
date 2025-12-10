package autoscaler

import (
	"context"
	"fmt"
	"log"

	"github.com/dxas90/scalebee/pkg/docker"
	"github.com/dxas90/scalebee/pkg/prometheus"
)

const (
	// CPUUpperLimit is the CPU percentage threshold for scaling up
	CPUUpperLimit = 75.0
	// CPULowerLimit is the CPU percentage threshold for scaling down
	CPULowerLimit = 20.0
	// MemoryUpperLimit is the memory percentage threshold for scaling up
	MemoryUpperLimit = 80.0
	// MemoryLowerLimit is the memory percentage threshold for scaling down
	MemoryLowerLimit = 20.0
)

// Config holds the autoscaler configuration
type Config struct {
	PrometheusURL    string
	CPUUpperLimit    float64
	CPULowerLimit    float64
	MemoryUpperLimit float64
	MemoryLowerLimit float64
}

// Autoscaler manages the autoscaling logic
type Autoscaler struct {
	config         *Config
	promClient     *prometheus.Client
	serviceManager *docker.ServiceManager
}

// NewAutoscaler creates a new autoscaler instance
func NewAutoscaler(config *Config) (*Autoscaler, error) {
	if config.CPUUpperLimit == 0 {
		config.CPUUpperLimit = CPUUpperLimit
	}
	if config.CPULowerLimit == 0 {
		config.CPULowerLimit = CPULowerLimit
	}
	if config.MemoryUpperLimit == 0 {
		config.MemoryUpperLimit = MemoryUpperLimit
	}
	if config.MemoryLowerLimit == 0 {
		config.MemoryLowerLimit = MemoryLowerLimit
	}

	promClient := prometheus.NewClient(config.PrometheusURL)

	serviceManager, err := docker.NewServiceManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create service manager: %w", err)
	}

	return &Autoscaler{
		config:         config,
		promClient:     promClient,
		serviceManager: serviceManager,
	}, nil
}

// Close cleans up resources
func (a *Autoscaler) Close() error {
	return a.serviceManager.Close()
}

// Run executes one iteration of the autoscaling loop
func (a *Autoscaler) Run(ctx context.Context) error {
	// Get both CPU and memory metrics concurrently for faster response
	cpuMetrics, memoryMetrics, err := a.promClient.GetServiceMetrics(ctx)
	if err != nil {
		log.Printf("Error: failed to get metrics: %v", err)
		return nil
	}

	log.Printf("Retrieved %d service CPU metrics from Prometheus", len(cpuMetrics))

	// Group CPU metrics by service name (aggregate multiple instances)
	serviceCPUMetrics := make(map[string][]float64)
	for _, m := range cpuMetrics {
		serviceCPUMetrics[m.ServiceName] = append(serviceCPUMetrics[m.ServiceName], m.CPUPercent)
	}

	// Process each service
	for serviceName, cpuValues := range serviceCPUMetrics {
		// Calculate average CPU
		var totalCPU float64
		for _, cpu := range cpuValues {
			totalCPU += cpu
		}
		avgCPU := totalCPU / float64(len(cpuValues))

		// Get memory percentage for this service
		avgMemory := memoryMetrics[serviceName]

		log.Printf("Service: %s, Avg CPU: %.2f%%, Avg Memory: %.2f%%", serviceName, avgCPU, avgMemory)

		// Get service configuration
		config, err := a.serviceManager.GetServiceConfig(ctx, serviceName)
		if err != nil {
			log.Printf("Warning: failed to get config for service %s: %v", serviceName, err)
			continue
		}

		if !config.AutoscaleEnabled {
			log.Printf("Service %s does not have autoscale label", serviceName)
			continue
		}

		log.Printf("Service %s has autoscale label", serviceName)

		// Apply default scaling (ensure within min/max bounds)
		if err := a.defaultScale(ctx, config); err != nil {
			log.Printf("Error during default scale for %s: %v", serviceName, err)
		}

		// Check if we need to scale based on CPU or Memory
		// Scale up if EITHER CPU or Memory exceeds upper threshold
		shouldScaleUp := false
		scaleUpReason := ""

		if avgCPU > a.config.CPUUpperLimit {
			shouldScaleUp = true
			scaleUpReason = fmt.Sprintf("CPU %.2f%% > %.0f%%", avgCPU, a.config.CPUUpperLimit)
		}

		if avgMemory > a.config.MemoryUpperLimit {
			shouldScaleUp = true
			if scaleUpReason != "" {
				scaleUpReason += fmt.Sprintf(" and Memory %.2f%% > %.0f%%", avgMemory, a.config.MemoryUpperLimit)
			} else {
				scaleUpReason = fmt.Sprintf("Memory %.2f%% > %.0f%%", avgMemory, a.config.MemoryUpperLimit)
			}
		}

		if shouldScaleUp {
			log.Printf("Service %s is above threshold: %s", serviceName, scaleUpReason)
			if err := a.scaleUp(ctx, serviceName); err != nil {
				log.Printf("Error scaling up %s: %v", serviceName, err)
			}
			continue // Don't check scale down if we're scaling up
		}

		// Scale down only if BOTH CPU and Memory are below lower threshold
		if avgCPU < a.config.CPULowerLimit && avgMemory < a.config.MemoryLowerLimit {
			log.Printf("Service %s is below threshold: CPU %.2f%% < %.0f%% and Memory %.2f%% < %.0f%%",
				serviceName, avgCPU, a.config.CPULowerLimit, avgMemory, a.config.MemoryLowerLimit)
			if err := a.scaleDown(ctx, serviceName); err != nil {
				log.Printf("Error scaling down %s: %v", serviceName, err)
			}
		}
	}

	return nil
}

// defaultScale ensures a service is within its min/max replica bounds
func (a *Autoscaler) defaultScale(ctx context.Context, config *docker.ServiceConfig) error {
	currentReplicas := int(config.CurrentReplicas)

	if config.MinReplicas > 0 && currentReplicas < config.MinReplicas {
		log.Printf("Service %s is below the minimum. Scaling to the minimum of %d",
			config.Name, config.MinReplicas)
		return a.serviceManager.ScaleService(ctx, config.Name, uint64(config.MinReplicas))
	}

	if config.MaxReplicas > 0 && currentReplicas > config.MaxReplicas {
		log.Printf("Service %s is above the maximum. Scaling to the maximum of %d",
			config.Name, config.MaxReplicas)
		return a.serviceManager.ScaleService(ctx, config.Name, uint64(config.MaxReplicas))
	}

	return nil
}

// scaleUp increases the replica count by 1 if within limits
func (a *Autoscaler) scaleUp(ctx context.Context, serviceName string) error {
	config, err := a.serviceManager.GetServiceConfig(ctx, serviceName)
	if err != nil {
		return err
	}

	if !config.AutoscaleEnabled {
		return nil
	}

	currentReplicas := int(config.CurrentReplicas)
	newReplicas := currentReplicas + 1

	if config.MaxReplicas > 0 && currentReplicas >= config.MaxReplicas {
		log.Printf("Service %s already has the maximum of %d replicas",
			serviceName, config.MaxReplicas)
		return nil
	}

	if config.MaxReplicas > 0 && newReplicas > config.MaxReplicas {
		log.Printf("Service %s would exceed maximum. Capping at %d replicas",
			serviceName, config.MaxReplicas)
		newReplicas = config.MaxReplicas
	}

	log.Printf("Scaling up service %s to %d", serviceName, newReplicas)
	return a.serviceManager.ScaleService(ctx, serviceName, uint64(newReplicas))
}

// scaleDown decreases the replica count by 1 if within limits
func (a *Autoscaler) scaleDown(ctx context.Context, serviceName string) error {
	config, err := a.serviceManager.GetServiceConfig(ctx, serviceName)
	if err != nil {
		return err
	}

	if !config.AutoscaleEnabled {
		return nil
	}

	currentReplicas := int(config.CurrentReplicas)
	newReplicas := currentReplicas - 1

	if config.MinReplicas > 0 && newReplicas < config.MinReplicas {
		log.Printf("Service %s has the minimum number of replicas (%d)",
			serviceName, config.MinReplicas)
		return nil
	}

	if currentReplicas == config.MinReplicas {
		log.Printf("Service %s has the minimum number of replicas", serviceName)
		return nil
	}

	log.Printf("Scaling down service %s to %d", serviceName, newReplicas)
	return a.serviceManager.ScaleService(ctx, serviceName, uint64(newReplicas))
}
