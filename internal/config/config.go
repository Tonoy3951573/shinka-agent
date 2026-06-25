// Package config handles loading and validating the agent configuration.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the agent's local configuration.
type Config struct {
	ServerURL         string  `yaml:"server_url"`         // Backend API base URL (e.g. https://your-server.com)
	APIKey            string  `yaml:"api_key"`            // Agent API key from /api/agents/register
	AgentName         string  `yaml:"agent_name"`         // Human-readable agent name
	MediaMTXURL       string  `yaml:"mediamtx_url"`       // MediaMTX server URL (e.g. rtsp://your-server.com)
	StateDir          string  `yaml:"state_dir"`          // Directory for persistent state (e.g. cached_config.json)
	ClipsDir          string  `yaml:"clips_dir"`          // Directory for recorded video clips
	MaxClipsSizeGB    float64 `yaml:"max_clips_size_gb"`   // Max disk usage threshold for clips in GB before pruning
	UploadConcurrency int     `yaml:"upload_concurrency"` // Max concurrent uploads
}

// Camera represents a camera assignment from the backend.
type Camera struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	RTSPUrl    string `json:"rtsp_url"`
	Username   string `json:"rtsp_username"`
	Password   string `json:"rtsp_password"`
	StreamPath string `json:"stream_path"`
	Location   string `json:"location"`
	Model      string `json:"model"`
}

// MediaMTXConfig holds MediaMTX server settings from the backend.
type MediaMTXConfig struct {
	RTSPPort int `json:"rtspPort"`
}

// configResponse matches the /api/agents/config JSON response.
type configResponse struct {
	AgentID        string         `json:"agentId"`
	OrganizationID string         `json:"organizationId"`
	Cameras        []Camera       `json:"cameras"`
	MediaMTX       MediaMTXConfig `json:"mediamtx"`
}

// Load reads and validates the agent configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config file %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid YAML in %s: %w", path, err)
	}

	// Validate required fields
	if cfg.ServerURL == "" {
		return nil, fmt.Errorf("server_url is required in config")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("api_key is required in config")
	}

	// Ensure no trailing slash
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")

	// Default MediaMTX URL to same host as server
	if cfg.MediaMTXURL == "" {
		// Extract host from server URL
		host := cfg.ServerURL
		host = strings.TrimPrefix(host, "https://")
		host = strings.TrimPrefix(host, "http://")
		if idx := strings.Index(host, ":"); idx > 0 {
			host = host[:idx]
		}
		if idx := strings.Index(host, "/"); idx > 0 {
			host = host[:idx]
		}
		cfg.MediaMTXURL = fmt.Sprintf("rtsp://%s", host)
	}

	// Set defaults
	if cfg.StateDir == "" {
		cfg.StateDir = "/var/lib/shinka-agent"
	}
	// Verify StateDir is writable. If not, fallback to local directory "."
	if err := os.MkdirAll(cfg.StateDir, 0755); err != nil {
		log.Printf("Warning: cannot create StateDir %s: %v; falling back to current directory", cfg.StateDir, err)
		cfg.StateDir = "."
	}

	if cfg.ClipsDir == "" {
		cfg.ClipsDir = filepath.Join(cfg.StateDir, "clips")
	}
	if cfg.MaxClipsSizeGB <= 0 {
		cfg.MaxClipsSizeGB = 2.0
	}
	if cfg.UploadConcurrency <= 0 {
		cfg.UploadConcurrency = 2
	}

	return &cfg, nil
}

// CacheConfig writes the camera configurations to a local file for offline recovery.
func CacheConfig(stateDir string, cameras []Camera, mediamtx MediaMTXConfig) error {
	if stateDir == "" {
		stateDir = "."
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}
	cachePath := filepath.Join(stateDir, "cached_config.json")
	data, err := json.MarshalIndent(configResponse{
		Cameras:  cameras,
		MediaMTX: mediamtx,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cachePath, data, 0600)
}

// LoadCachedConfig reads the camera configurations from the local cache file.
func LoadCachedConfig(stateDir string) ([]Camera, MediaMTXConfig, error) {
	if stateDir == "" {
		stateDir = "."
	}
	cachePath := filepath.Join(stateDir, "cached_config.json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, MediaMTXConfig{}, err
	}
	var cached configResponse
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, MediaMTXConfig{}, err
	}
	return cached.Cameras, cached.MediaMTX, nil
}

// FetchCameraConfig retrieves the agent's camera assignments from the backend.
func FetchCameraConfig(cfg *Config) ([]Camera, MediaMTXConfig, error) {
	url := fmt.Sprintf("%s/api/agents/config", cfg.ServerURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, MediaMTXConfig{}, err
	}

	req.Header.Set("x-agent-api-key", cfg.APIKey)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, MediaMTXConfig{}, fmt.Errorf("cannot reach server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, MediaMTXConfig{}, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var result configResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, MediaMTXConfig{}, fmt.Errorf("invalid JSON response: %w", err)
	}

	// Cache successful config locally
	_ = CacheConfig(cfg.StateDir, result.Cameras, result.MediaMTX)

	return result.Cameras, result.MediaMTX, nil
}
