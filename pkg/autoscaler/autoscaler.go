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
	CPUUpperLimit = 85.0
	// CPULowerLimit is the CPU percentage threshold for scaling down
	CPULowerLimit = 25.0
)

// Config holds the autoscaler configuration
type Config struct {
	PrometheusURL string
	CPUUpperLimit float64
	CPULowerLimit float64
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
	// Get metrics from Prometheus
	metrics, err := a.promClient.GetServiceCPUMetrics(ctx)
	if err != nil {
		return fmt.Errorf("failed to get metrics: %w", err)
	}

	log.Printf("Retrieved %d service metrics from Prometheus", len(metrics))

	// Group metrics by service name (aggregate multiple instances)
	serviceMetrics := make(map[string][]float64)
	for _, m := range metrics {
		serviceMetrics[m.ServiceName] = append(serviceMetrics[m.ServiceName], m.CPUPercent)
	}

	// Process each service
	for serviceName, cpuValues := range serviceMetrics {
		// Calculate average CPU
		var totalCPU float64
		for _, cpu := range cpuValues {
			totalCPU += cpu
		}
		avgCPU := totalCPU / float64(len(cpuValues))

		log.Printf("Service: %s, Avg CPU: %.2f%%", serviceName, avgCPU)

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

		// Check if we need to scale based on CPU
		if avgCPU > a.config.CPUUpperLimit {
			log.Printf("Service %s is above %.0f%% CPU usage", serviceName, a.config.CPUUpperLimit)
			if err := a.scaleUp(ctx, serviceName); err != nil {
				log.Printf("Error scaling up %s: %v", serviceName, err)
			}
		} else if avgCPU < a.config.CPULowerLimit {
			log.Printf("Service %s is below %.0f%% CPU usage", serviceName, a.config.CPULowerLimit)
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
