package docker

import (
	"context"
	"fmt"
	"strconv"

	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
)

// ServiceManager handles Docker Swarm service operations
type ServiceManager struct {
	client *client.Client
}

// ServiceConfig holds autoscaling configuration for a service
type ServiceConfig struct {
	Name             string
	CurrentReplicas  uint64
	MinReplicas      int
	MaxReplicas      int
	AutoscaleEnabled bool
}

// NewServiceManager creates a new Docker service manager
func NewServiceManager() (*ServiceManager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &ServiceManager{
		client: cli,
	}, nil
}

// Close closes the Docker client connection
func (sm *ServiceManager) Close() error {
	return sm.client.Close()
}

// GetServiceConfig retrieves the autoscaling configuration for a service
func (sm *ServiceManager) GetServiceConfig(ctx context.Context, serviceName string) (*ServiceConfig, error) {
	service, _, err := sm.client.ServiceInspectWithRaw(ctx, serviceName, swarm.ServiceInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect service %s: %w", serviceName, err)
	}

	config := &ServiceConfig{
		Name:             serviceName,
		MinReplicas:      0,
		MaxReplicas:      0,
		AutoscaleEnabled: false,
	}

	// Check if autoscaling is enabled
	if service.Spec.Labels != nil {
		if val, ok := service.Spec.Labels["swarm.autoscaler"]; ok && val == "true" {
			config.AutoscaleEnabled = true
		}

		// Get minimum replicas
		if val, ok := service.Spec.Labels["swarm.autoscaler.minimum"]; ok {
			if min, err := strconv.Atoi(val); err == nil {
				config.MinReplicas = min
			}
		}

		// Get maximum replicas
		if val, ok := service.Spec.Labels["swarm.autoscaler.maximum"]; ok {
			if max, err := strconv.Atoi(val); err == nil {
				config.MaxReplicas = max
			}
		}
	}

	// Get current replicas
	if service.Spec.Mode.Replicated != nil && service.Spec.Mode.Replicated.Replicas != nil {
		config.CurrentReplicas = *service.Spec.Mode.Replicated.Replicas
	}

	return config, nil
}

// ScaleService scales a service to the specified number of replicas
func (sm *ServiceManager) ScaleService(ctx context.Context, serviceName string, replicas uint64) error {
	service, _, err := sm.client.ServiceInspectWithRaw(ctx, serviceName, swarm.ServiceInspectOptions{})
	if err != nil {
		return fmt.Errorf("failed to inspect service %s: %w", serviceName, err)
	}

	// Update the replica count
	if service.Spec.Mode.Replicated == nil {
		return fmt.Errorf("service %s is not in replicated mode", serviceName)
	}

	service.Spec.Mode.Replicated.Replicas = &replicas

	// Update the service
	_, err = sm.client.ServiceUpdate(
		ctx,
		service.ID,
		service.Version,
		service.Spec,
		swarm.ServiceUpdateOptions{},
	)

	if err != nil {
		return fmt.Errorf("failed to update service %s: %w", serviceName, err)
	}

	return nil
}
