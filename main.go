package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dxas90/scalebee/pkg/autoscaler"
	"github.com/dxas90/scalebee/pkg/metrics"
)

func main() {
	// Get configuration from environment variables
	prometheusURL := getEnv("PROMETHEUS_URL", "http://prometheus:9090")
	loopEnabled := getEnv("LOOP", "yes") == "yes"
	intervalSeconds := getEnvInt("INTERVAL_SECONDS", 13)
	metricsPort := getEnv("METRICS_PORT", "9090")
	metricsEnabled := getEnv("METRICS_ENABLED", "yes") == "yes"

	log.Printf("ScaleBee - Docker Swarm Autoscaler")
	log.Printf("Prometheus URL: %s", prometheusURL)
	log.Printf("Loop enabled: %v", loopEnabled)
	log.Printf("Interval: %d seconds", intervalSeconds)
	log.Printf("Metrics exporter enabled: %v", metricsEnabled)
	if metricsEnabled {
		log.Printf("Metrics port: %s", metricsPort)
	}

	// Setup signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal, stopping...")
		cancel()
	}()

	// Start metrics exporter if enabled
	var metricsExporter *metrics.Exporter
	if metricsEnabled {
		var err error
		metricsExporter, err = metrics.NewExporter(10 * time.Second)
		if err != nil {
			log.Fatalf("Failed to create metrics exporter: %v", err)
		}
		defer metricsExporter.Close()

		// Start metrics collection in background
		go metricsExporter.Start(ctx)

		// Start HTTP server for metrics
		mux := http.NewServeMux()
		mux.Handle("/metrics", metricsExporter)
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		})

		server := &http.Server{
			Addr:    ":" + metricsPort,
			Handler: mux,
		}

		go func() {
			log.Printf("Starting metrics server on port %s", metricsPort)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("Metrics server error: %v", err)
			}
		}()

		// Shutdown server on context cancellation
		go func() {
			<-ctx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				log.Printf("Error shutting down metrics server: %v", err)
			}
		}()
	}

	// Create autoscaler
	config := &autoscaler.Config{
		PrometheusURL:    prometheusURL,
		CPUUpperLimit:    getEnvFloat("CPU_PERCENTAGE_UPPER_LIMIT", 75.0),
		CPULowerLimit:    getEnvFloat("CPU_PERCENTAGE_LOWER_LIMIT", 20.0),
		MemoryUpperLimit: getEnvFloat("MEMORY_PERCENTAGE_UPPER_LIMIT", 80.0),
		MemoryLowerLimit: getEnvFloat("MEMORY_PERCENTAGE_LOWER_LIMIT", 20.0),
	}

	scaler, err := autoscaler.NewAutoscaler(config)
	if err != nil {
		log.Fatalf("Failed to create autoscaler: %v", err)
	}
	defer scaler.Close()

	// Wait for Prometheus to be ready (up to 10 retries with exponential backoff)
	if err := scaler.PrometheusClient().WaitForPrometheus(ctx, 10); err != nil {
		log.Fatalf("Failed to connect to Prometheus: %v", err)
	}

	log.Printf("CPU Upper Limit: %.0f%%", config.CPUUpperLimit)
	log.Printf("CPU Lower Limit: %.0f%%", config.CPULowerLimit)
	log.Printf("Memory Upper Limit: %.0f%%", config.MemoryUpperLimit)
	log.Printf("Memory Lower Limit: %.0f%%", config.MemoryLowerLimit)

	// Run the autoscaler
	log.Println("Starting autoscaler...")

	// First run
	if err := scaler.Run(ctx); err != nil {
		log.Printf("Error during autoscaling run: %v", err)
	}

	if !loopEnabled {
		log.Println("Loop disabled, exiting after one run")
		return
	}

	// Continuous loop
	ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down autoscaler")
			return
		case <-ticker.C:
			log.Printf("Waiting %d seconds for the next check...", intervalSeconds)
			if err := scaler.Run(ctx); err != nil {
				log.Printf("Error during autoscaling run: %v", err)
			}
		}
	}
}

// getEnv gets an environment variable with a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvInt gets an integer environment variable with a default value
func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		var result int
		if _, err := fmt.Sscanf(value, "%d", &result); err == nil {
			return result
		}
	}
	return defaultValue
}

// getEnvFloat gets a float environment variable with a default value
func getEnvFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		var result float64
		if _, err := fmt.Sscanf(value, "%f", &result); err == nil {
			return result
		}
	}
	return defaultValue
}
