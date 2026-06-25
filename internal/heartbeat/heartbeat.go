// Package heartbeat sends periodic status updates to the backend.
package heartbeat

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"
)

var startTime = time.Now()

func getOSInfo() string {
	return runtime.GOOS + " " + runtime.GOARCH
}

func getDeviceFingerprint() string {
	hn, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hn
}

// CameraStatus reports a single camera's state.
type CameraStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "online", "offline", "error"
}

// Config for the heartbeat sender.
type Config struct {
	ServerURL  string
	APIKey     string
	Interval   time.Duration
	GetCameras func() []CameraStatus
}

// Heartbeat periodically reports agent health to the backend.
type Heartbeat struct {
	config Config
	stopCh chan struct{}
}

// heartbeatPayload is the JSON body sent to /api/agents/heartbeat.
type heartbeatPayload struct {
	Version           string         `json:"version,omitempty"`
	Cameras           []CameraStatus `json:"cameras"`
	DeviceFingerprint string         `json:"device_fingerprint"`
	OSInfo            string         `json:"os_info"`
	UptimeSeconds     int64          `json:"uptime_seconds"`
}

// New creates a heartbeat sender.
func New(cfg Config) *Heartbeat {
	if cfg.Interval == 0 {
		cfg.Interval = 10 * time.Second
	}
	return &Heartbeat{
		config: cfg,
		stopCh: make(chan struct{}),
	}
}

// Start begins the heartbeat loop. Blocks until Stop() is called.
func (h *Heartbeat) Start() {
	log.Printf("[heartbeat] Starting (every %s)", h.config.Interval)

	// Send immediately on start
	h.send()

	ticker := time.NewTicker(h.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.send()
		case <-h.stopCh:
			log.Println("[heartbeat] Stopped")
			return
		}
	}
}

// Stop terminates the heartbeat loop.
func (h *Heartbeat) Stop() {
	close(h.stopCh)
}

func (h *Heartbeat) send() {
	cameras := h.config.GetCameras()

	payload := heartbeatPayload{
		Version:           "1.0.0",
		Cameras:           cameras,
		DeviceFingerprint: getDeviceFingerprint(),
		OSInfo:            getOSInfo(),
		UptimeSeconds:     int64(time.Since(startTime).Seconds()),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[heartbeat] Marshal error: %v", err)
		return
	}

	url := h.config.ServerURL + "/api/agents/heartbeat"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		log.Printf("[heartbeat] Request error: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-agent-api-key", h.config.APIKey)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[heartbeat] Send error: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("[heartbeat] Server returned %d", resp.StatusCode)
	}
}
