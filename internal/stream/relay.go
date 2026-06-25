// Package stream handles RTSP relay via FFmpeg.
// Each relay reads from a camera's RTSP URL and pushes to MediaMTX.
package stream

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RelayConfig holds the configuration for a single stream relay.
type RelayConfig struct {
	CameraID    string
	CameraName  string
	RTSPUrl     string
	Username    string
	Password    string
	StreamPath  string
	MediaMTXURL string // e.g. rtsp://server-host
	RTSPPort    int    // e.g. 8554
	ClipsDir    string
}

// Relay manages an FFmpeg process that relays RTSP to MediaMTX.
type Relay struct {
	config RelayConfig
	cmd    *exec.Cmd
	mu     sync.Mutex
	status string // "online", "offline", "error"
	stopCh chan struct{}
}

// NewRelay creates a new stream relay for a camera.
func NewRelay(cfg RelayConfig) *Relay {
	if cfg.RTSPPort == 0 {
		cfg.RTSPPort = 8554
	}
	return &Relay{
		config: cfg,
		status: "offline",
		stopCh: make(chan struct{}),
	}
}

// CameraID returns the camera ID this relay is managing.
func (r *Relay) CameraID() string {
	return r.config.CameraID
}

// Status returns the current relay status.
func (r *Relay) Status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// buildSourceURL constructs the authenticated RTSP source URL.
func (r *Relay) buildSourceURL() string {
	url := r.config.RTSPUrl

	// If credentials are provided, inject them into the URL
	if r.config.Username != "" {
		// rtsp://user:pass@host:port/path
		url = strings.Replace(url, "rtsp://", fmt.Sprintf("rtsp://%s:%s@", r.config.Username, r.config.Password), 1)
	}

	return url
}

// buildTargetURL constructs the MediaMTX RTSP publish URL.
func (r *Relay) buildTargetURL() string {
	return fmt.Sprintf("%s:%d/%s", r.config.MediaMTXURL, r.config.RTSPPort, r.config.StreamPath)
}

// Start begins the FFmpeg relay process with automatic restart on failure.
func (r *Relay) Start() {
	log.Printf("[relay:%s] Starting stream relay: %s → %s", r.config.CameraName, r.config.RTSPUrl, r.config.StreamPath)

	clipsDir := r.config.ClipsDir
	if clipsDir == "" {
		clipsDir = "clips"
	}

	// Ensure clips output directory exists
	// Ensure clips output directory exists
	if err := os.MkdirAll(clipsDir, 0755); err != nil {
		log.Printf("[relay:%s] Fatal: failed to create clips directory: %v", r.config.CameraName, err)
		return
	}

	for {
		select {
		case <-r.stopCh:
			return
		default:
		}

		sourceURL := r.buildSourceURL()
		targetURL := r.buildTargetURL()

		// FFmpeg command:
		//   -rtsp_transport tcp     : Use TCP for reliable transport
		//   -timeout 5000000        : 5-second connect/read timeout (microseconds)
		//   -i <source>             : Input RTSP stream
		//   -c copy                 : No transcoding (passthrough)
		//   -f rtsp                 : Output format RTSP
		//   -rtsp_transport tcp     : Output also uses TCP
		//   <target>                : MediaMTX RTSP publish URL
		//   -f segment ...          : Secondary output to segment file stream
		r.cmd = exec.Command("ffmpeg",
			"-hide_banner",
			"-loglevel", "warning",
			"-rtsp_transport", "tcp",
			"-timeout", "5000000",
			"-i", sourceURL,
			// Relay output
			"-c", "copy",
			"-f", "rtsp",
			"-rtsp_transport", "tcp",
			targetURL,
			// Local segment output
			"-c", "copy",
			"-f", "segment",
			"-segment_time", "30",
			"-segment_format", "mp4",
			"-reset_timestamps", "1",
			"-strftime", "1",
			filepath.Join(clipsDir, "clip_"+r.config.CameraID+"_%Y%m%d_%H%M%S.mp4"),
		)

		r.mu.Lock()
		r.status = "online"
		r.mu.Unlock()

		log.Printf("[relay:%s] FFmpeg process started", r.config.CameraName)

		err := r.cmd.Run()

		r.mu.Lock()
		r.status = "error"
		r.mu.Unlock()

		if err != nil {
			log.Printf("[relay:%s] FFmpeg exited: %v", r.config.CameraName, err)
		}

		// Check if we should stop
		select {
		case <-r.stopCh:
			return
		default:
		}

		// Wait before restarting
		log.Printf("[relay:%s] Restarting in 5 seconds...", r.config.CameraName)
		select {
		case <-time.After(5 * time.Second):
		case <-r.stopCh:
			return
		}
	}
}

// Stop terminates the FFmpeg process and the relay loop.
func (r *Relay) Stop() {
	close(r.stopCh)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	r.status = "offline"

	log.Printf("[relay:%s] Stopped", r.config.CameraName)
}
