// Package main is the entry point for the Shinka Dynamics local camera agent.
//
// The agent runs on customer premises (Windows/Linux/macOS) and is responsible for:
//   1. Authenticating with the backend using its API key
//   2. Fetching camera assignments from /api/agents/config
//   3. Relaying RTSP streams to the MediaMTX server via FFmpeg
//   4. Sending periodic heartbeats with camera status
//
// Usage:
//   ./shinka-agent -config agent.yml
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"shinka-agent/internal/config"
	"shinka-agent/internal/heartbeat"
	"shinka-agent/internal/stream"
	"shinka-agent/internal/uploader"
)

func main() {
	configPath := flag.String("config", "agent.yml", "Path to agent configuration file")
	flag.Parse()

	// ── Load Configuration ────────────────────────────────────────────
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	fmt.Println("╔══════════════════════════════════════════════════════╗")
	fmt.Println("║  Shinka Dynamics — Local Camera Agent                ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")
	fmt.Printf("  Server:   %s\n", cfg.ServerURL)
	fmt.Printf("  Agent ID: %s\n", cfg.AgentName)
	fmt.Println()

	// ── Fetch Camera Config with Offline Fallback ─────────────────────
	var cameras []config.Camera
	var mediamtxCfg config.MediaMTXConfig
	var fetchErr error

	// Try fetching from the backend
	cameras, mediamtxCfg, fetchErr = config.FetchCameraConfig(cfg)
	if fetchErr != nil {
		log.Printf("Failed to fetch camera config from backend: %v", fetchErr)
		log.Println("Attempting to load cached configuration...")
		var cacheErr error
		cameras, mediamtxCfg, cacheErr = config.LoadCachedConfig(cfg.StateDir)
		if cacheErr != nil {
			log.Printf("Failed to load cached configuration: %v", cacheErr)
			log.Println("Entering retry loop to wait for backend config...")

			// Backoff loop to retry fetching config
			backoff := 5 * time.Second
			for {
				time.Sleep(backoff)
				cameras, mediamtxCfg, fetchErr = config.FetchCameraConfig(cfg)
				if fetchErr == nil {
					log.Printf("Successfully fetched camera config from backend after retry")
					break
				}
				log.Printf("Retry config fetch failed: %v. Retrying in %s...", fetchErr, backoff)
				backoff *= 2
				if backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
			}
		} else {
			log.Printf("Successfully loaded %d cameras from local cache", len(cameras))
		}
	} else {
		log.Printf("Fetched %d camera assignments from backend", len(cameras))
	}

	// ── Start Stream Relays ───────────────────────────────────────────
	var wg sync.WaitGroup
	relays := make([]*stream.Relay, 0, len(cameras))

	for _, cam := range cameras {
		if cam.RTSPUrl == "" {
			log.Printf("Skipping camera %s (%s): no RTSP URL", cam.ID, cam.Name)
			continue
		}

		relay := stream.NewRelay(stream.RelayConfig{
			CameraID:    cam.ID,
			CameraName:  cam.Name,
			RTSPUrl:     cam.RTSPUrl,
			Username:    cam.Username,
			Password:    cam.Password,
			StreamPath:  cam.StreamPath,
			MediaMTXURL: cfg.MediaMTXURL,
			RTSPPort:    mediamtxCfg.RTSPPort,
			ClipsDir:    cfg.ClipsDir,
		})

		wg.Add(1)
		go func() {
			defer wg.Done()
			relay.Start()
		}()

		relays = append(relays, relay)
	}

	// ── Start Heartbeat ───────────────────────────────────────────────
	hb := heartbeat.New(heartbeat.Config{
		ServerURL:  cfg.ServerURL,
		APIKey:     cfg.APIKey,
		Interval:   10 * time.Second,
		GetCameras: func() []heartbeat.CameraStatus {
			statuses := make([]heartbeat.CameraStatus, len(relays))
			for i, r := range relays {
				statuses[i] = heartbeat.CameraStatus{
					ID:     r.CameraID(),
					Status: r.Status(),
				}
			}
			return statuses
		},
	})

	go hb.Start()

	// ── Start Uploader ────────────────────────────────────────────────
	up := uploader.New(uploader.Config{
		ServerURL:         cfg.ServerURL,
		APIKey:            cfg.APIKey,
		Interval:          5 * time.Second,
		ClipsDir:          cfg.ClipsDir,
		MaxClipsSizeGB:    cfg.MaxClipsSizeGB,
		UploadConcurrency: cfg.UploadConcurrency,
	})

	go up.Start()

	// ── Graceful Shutdown ─────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	log.Printf("Received %s, shutting down...", sig)

	up.Stop()
	hb.Stop()
	for _, relay := range relays {
		relay.Stop()
	}
	wg.Wait()

	log.Println("Agent stopped cleanly.")
}
