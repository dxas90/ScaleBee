package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Client represents a Prometheus API client
type Client struct {
	baseURL string
	client  *http.Client
}

// ServiceMetric represents CPU and memory metrics for a Docker service
type ServiceMetric struct {
	ServiceName   string
	CPUPercent    float64
	MemoryPercent float64
}

// PrometheusResponse represents the structure of Prometheus query API response
type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// NewClient creates a new Prometheus client
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// WaitForPrometheus waits for Prometheus to be ready with exponential backoff
func (c *Client) WaitForPrometheus(ctx context.Context, maxRetries int) error {
	log.Printf("Waiting for Prometheus at %s to be ready...", c.baseURL)
	
	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Try to query Prometheus
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/-/ready", c.baseURL), nil)
		if err == nil {
			resp, err := c.client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					log.Printf("Prometheus is ready")
					return nil
				}
			}
		}
		
		if attempt < maxRetries {
			// Exponential backoff: 2, 4, 8, 16, 32 seconds (max 32s)
			waitTime := time.Duration(min(1<<uint(attempt), 32)) * time.Second
			log.Printf("Prometheus not ready (attempt %d/%d), retrying in %v...", attempt, maxRetries, waitTime)
			
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(waitTime):
			}
		}
	}
	
	return fmt.Errorf("prometheus did not become ready after %d attempts", maxRetries)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GetServiceCPUMetrics queries Prometheus for CPU metrics of Docker Swarm services
func (c *Client) GetServiceCPUMetrics(ctx context.Context) ([]ServiceMetric, error) {
	// Build Prometheus query to get CPU metrics per service
	// Using the new metric format from ScaleBee metrics exporter
	query := `avg(container_cpu_usage_percent) BY (service)`

	// Build the URL
	apiURL := fmt.Sprintf("%s/api/v1/query", c.baseURL)
	params := url.Values{}
	params.Add("query", query)

	fullURL := fmt.Sprintf("%s?%s", apiURL, params.Encode())

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Execute request
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("prometheus returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var promResp prometheusResponse
	if err := json.NewDecoder(resp.Body).Decode(&promResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed with status: %s", promResp.Status)
	}

	// Extract metrics
	metrics := make([]ServiceMetric, 0)
	for _, result := range promResp.Data.Result {
		serviceName, ok := result.Metric["service"]
		if !ok {
			continue
		}

		// The value is [timestamp, "value_as_string"]
		if len(result.Value) < 2 {
			continue
		}

		cpuStr, ok := result.Value[1].(string)
		if !ok {
			continue
		}

		cpuPercent, err := strconv.ParseFloat(cpuStr, 64)
		if err != nil {
			continue
		}

		metrics = append(metrics, ServiceMetric{
			ServiceName: serviceName,
			CPUPercent:  cpuPercent,
		})
	}

	return metrics, nil
}

// GetServiceMemoryMetrics queries Prometheus for memory metrics of Docker Swarm services
func (c *Client) GetServiceMemoryMetrics(ctx context.Context) (map[string]float64, error) {
	// Query for memory usage percentage per service
	// Calculate as (memory_usage / memory_limit) * 100
	query := `(avg(container_memory_usage_mb) BY (service) / avg(container_memory_limit_mb) BY (service)) * 100`

	apiURL := fmt.Sprintf("%s/api/v1/query", c.baseURL)
	params := url.Values{}
	params.Add("query", query)

	fullURL := fmt.Sprintf("%s?%s", apiURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("prometheus returned status %d: %s", resp.StatusCode, string(body))
	}

	var promResp prometheusResponse
	if err := json.NewDecoder(resp.Body).Decode(&promResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed with status: %s", promResp.Status)
	}

	// Extract memory metrics into a map
	memoryMetrics := make(map[string]float64)
	for _, result := range promResp.Data.Result {
		serviceName, ok := result.Metric["service"]
		if !ok {
			continue
		}

		if len(result.Value) < 2 {
			continue
		}

		memoryStr, ok := result.Value[1].(string)
		if !ok {
			continue
		}

		memoryPercent, err := strconv.ParseFloat(memoryStr, 64)
		if err != nil {
			continue
		}

		memoryMetrics[serviceName] = memoryPercent
	}

	return memoryMetrics, nil
}

// GetServiceMetrics fetches CPU and memory metrics concurrently for better performance
func (c *Client) GetServiceMetrics(ctx context.Context) ([]ServiceMetric, map[string]float64, error) {
	var (
		cpuMetrics    []ServiceMetric
		memoryMetrics map[string]float64
		cpuErr        error
		memoryErr     error
		wg            sync.WaitGroup
	)

	wg.Add(2)

	// Fetch CPU metrics in a goroutine
	go func() {
		defer wg.Done()
		cpuMetrics, cpuErr = c.GetServiceCPUMetrics(ctx)
	}()

	// Fetch memory metrics in a goroutine
	go func() {
		defer wg.Done()
		memoryMetrics, memoryErr = c.GetServiceMemoryMetrics(ctx)
	}()

	// Wait for both to complete
	wg.Wait()

	// Return CPU error if it failed (critical)
	if cpuErr != nil {
		return nil, nil, cpuErr
	}

	// If memory fails, log but continue with empty map
	if memoryErr != nil {
		memoryMetrics = make(map[string]float64)
	}

	return cpuMetrics, memoryMetrics, nil
}
