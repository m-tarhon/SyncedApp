package main

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

func loadConfig() (AppConfig, error) {
	if err := godotenv.Load(); err != nil {
		return AppConfig{}, fmt.Errorf("load .env: %w", err)
	}

	config := AppConfig{
		ListenAddr:                 getEnvOrDefault("LISTEN_ADDR", ":5000"),
		Prefix:                     getEnvOrDefault("PROMETHEUS_PROXY_PREFIX", "/prometheus/"),
		CouchbaseConnectionString:  os.Getenv("COUCHBASE_CONNECTION_STRING"),
		Username:                   os.Getenv("USERNAME"),
		Password:                   os.Getenv("PASSWORD"),
		MetadataBucketName:         os.Getenv("METADATA_BUCKET_NAME"),
		PrometheusConnectionString: os.Getenv("PROMETHEUS_CONNECTION_STRING"),
	}

	if config.CouchbaseConnectionString == "" {
		return AppConfig{}, fmt.Errorf("COUCHBASE_CONNECTION_STRING is required")
	}
	if config.Username == "" {
		return AppConfig{}, fmt.Errorf("USERNAME is required")
	}
	if config.Password == "" {
		return AppConfig{}, fmt.Errorf("PASSWORD is required")
	}
	if config.MetadataBucketName == "" {
		return AppConfig{}, fmt.Errorf("METADATA_BUCKET_NAME is required")
	}
	if config.PrometheusConnectionString == "" {
		return AppConfig{}, fmt.Errorf("PROMETHEUS_CONNECTION_STRING is required")
	}

	return config, nil
}

func getEnvOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
