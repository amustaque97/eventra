package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds the application configuration
type Config struct {
	NodeID        string
	HTTPPort      string
	DataDir       string
	EtcdEndpoints []string
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	cfg := &Config{
		NodeID:   getEnv("NODE_ID", "event-store-0"),
		HTTPPort: getEnv("HTTP_PORT", "8080"),
		DataDir:  getEnv("DATA_DIR", "/data/wal"),
	}

	// Parse etcd endpoints
	etcdEndpointsStr := getEnv("ETCD_ENDPOINTS", "localhost:2379")
	cfg.EtcdEndpoints = strings.Split(etcdEndpointsStr, ",")

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.NodeID == "" {
		return fmt.Errorf("NODE_ID is required")
	}

	if c.HTTPPort == "" {
		return fmt.Errorf("HTTP_PORT is required")
	}

	// Validate port is a number
	if _, err := strconv.Atoi(c.HTTPPort); err != nil {
		return fmt.Errorf("HTTP_PORT must be a valid port number")
	}

	if c.DataDir == "" {
		return fmt.Errorf("DATA_DIR is required")
	}

	if len(c.EtcdEndpoints) == 0 {
		return fmt.Errorf("ETCD_ENDPOINTS is required")
	}

	return nil
}

// GetNodeAddress returns the HTTP address of this node
func (c *Config) GetNodeAddress() string {
	// In Kubernetes, use the pod name as hostname
	hostname := os.Getenv("HOSTNAME")
	if hostname == "" {
		hostname = c.NodeID
	}

	// Use headless service for StatefulSet
	serviceName := getEnv("SERVICE_NAME", "event-store")
	namespace := getEnv("NAMESPACE", "default")

	// Format: <pod-name>.<service-name>.<namespace>.svc.cluster.local:<port>
	return fmt.Sprintf("%s.%s.%s.svc.cluster.local:%s",
		hostname, serviceName, namespace, c.HTTPPort)
}

// getEnv gets an environment variable with a default value
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
