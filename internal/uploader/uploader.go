// Package uploader scans local directory for completed video segments,
// creates corresponding interaction records on the server, and uploads the clips.
package uploader

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Config struct {
	ServerURL         string
	APIKey            string
	Interval          time.Duration
	ClipsDir          string
	MaxClipsSizeGB    float64
	UploadConcurrency int
}

type Uploader struct {
	config Config
	stopCh chan struct{}
	wg     sync.WaitGroup
	sem    chan struct{}
}

type interactionResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type fileInfo struct {
	path      string
	size      int64
	timestamp time.Time
}

func New(cfg Config) *Uploader {
	if cfg.Interval == 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.ClipsDir == "" {
		cfg.ClipsDir = "clips"
	}
	if cfg.MaxClipsSizeGB <= 0 {
		cfg.MaxClipsSizeGB = 2.0
	}
	if cfg.UploadConcurrency <= 0 {
		cfg.UploadConcurrency = 2
	}
	return &Uploader{
		config: cfg,
		stopCh: make(chan struct{}),
		sem:    make(chan struct{}, cfg.UploadConcurrency),
	}
}

func (u *Uploader) Start() {
	u.wg.Add(1)
	defer u.wg.Done()

	log.Printf("[uploader] Starting video segment uploader (interval: %s, concurrency: %d, max size: %.1f GB)",
		u.config.Interval, u.config.UploadConcurrency, u.config.MaxClipsSizeGB)

	// Ensure clips directory exists
	if err := os.MkdirAll(u.config.ClipsDir, 0755); err != nil {
		log.Printf("[uploader] Failed to create clips directory: %v", err)
	}

	ticker := time.NewTicker(u.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			u.scanAndUpload()
		case <-u.stopCh:
			log.Println("[uploader] Stopped")
			return
		}
	}
}

func (u *Uploader) Stop() {
	close(u.stopCh)
	u.wg.Wait()
}

// parseFilename parses "clip_cam_ID_YYYYMMDD_HHMMSS.mp4"
func parseFilename(name string) (string, time.Time, error) {
	base := strings.TrimSuffix(name, ".mp4")
	base = strings.TrimSuffix(base, ".upload") // handle temporary upload names too
	parts := strings.Split(base, "_")
	if len(parts) < 5 {
		return "", time.Time{}, fmt.Errorf("filename must have at least 5 parts separated by underscore")
	}

	// Reconstruct camera ID by joining parts between first ("clip") and last two (date, time)
	camIDParts := parts[1 : len(parts)-2]
	cameraID := strings.Join(camIDParts, "_")

	datePart := parts[len(parts)-2]
	timePart := parts[len(parts)-1]
	timestampStr := datePart + "_" + timePart

	// Parse in local time
	t, err := time.ParseInLocation("20060102_150405", timestampStr, time.Local)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to parse timestamp '%s': %w", timestampStr, err)
	}

	return cameraID, t, nil
}

func (u *Uploader) pruneOldestClips() {
	maxSizeBytes := int64(u.config.MaxClipsSizeGB * 1024 * 1024 * 1024)
	if maxSizeBytes <= 0 {
		return
	}

	files, err := os.ReadDir(u.config.ClipsDir)
	if err != nil {
		return
	}

	var clips []fileInfo
	var totalSize int64

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if !strings.HasPrefix(name, "clip_") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		_, ts, err := parseFilename(name)
		if err != nil {
			ts = info.ModTime()
		}
		clips = append(clips, fileInfo{
			path:      filepath.Join(u.config.ClipsDir, name),
			size:      info.Size(),
			timestamp: ts,
		})
		totalSize += info.Size()
	}

	if totalSize <= maxSizeBytes {
		return
	}

	// Sort clips by timestamp (oldest first)
	for i := 0; i < len(clips); i++ {
		for j := i + 1; j < len(clips); j++ {
			if clips[i].timestamp.After(clips[j].timestamp) {
				clips[i], clips[j] = clips[j], clips[i]
			}
		}
	}

	log.Printf("[uploader] Storage warning: clips directory size (%d MB) exceeds limit (%d MB). Pruning oldest clips...", totalSize/(1024*1024), maxSizeBytes/(1024*1024))

	for _, clip := range clips {
		if totalSize <= int64(float64(maxSizeBytes)*0.8) {
			break
		}
		if err := os.Remove(clip.path); err == nil {
			totalSize -= clip.size
			log.Printf("[uploader] Pruned oldest clip: %s", filepath.Base(clip.path))
		} else {
			log.Printf("[uploader] Failed to prune %s: %v", filepath.Base(clip.path), err)
		}
	}
}

func (u *Uploader) scanAndUpload() {
	// Prune oldest files if they exceed max space
	u.pruneOldestClips()

	files, err := os.ReadDir(u.config.ClipsDir)
	if err != nil {
		log.Printf("[uploader] Failed to read clips directory: %v", err)
		return
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		name := file.Name()
		// Only process completed .mp4 segments, ignore temporary .tmp files
		if !strings.HasPrefix(name, "clip_") || !strings.HasSuffix(name, ".mp4") {
			continue
		}

		info, err := file.Info()
		if err != nil {
			log.Printf("[uploader] Failed to get file info for %s: %v", name, err)
			continue
		}

		// If the file was modified very recently (within 15 seconds), it's likely
		// the active segment currently being written by FFmpeg. Skip it.
		if time.Since(info.ModTime()) < 15*time.Second {
			continue
		}

		cameraID, startTime, err := parseFilename(name)
		if err != nil {
			log.Printf("[uploader] Skipping file %s: %v", name, err)
			continue
		}

		filePath := filepath.Join(u.config.ClipsDir, name)

		// Try to acquire upload slot (non-blocking)
		select {
		case u.sem <- struct{}{}:
			// Acquired slot
		default:
			// No slots available, break the loop to process in future ticks
			return
		}

		// Rename to .upload suffix to gain exclusive ownership and avoid double processing
		uploadPath := filePath + ".upload"
		if err := os.Rename(filePath, uploadPath); err != nil {
			// Free slot and retry next tick
			<-u.sem
			continue
		}

		log.Printf("[uploader] Uploading completed segment: %s (camera: %s, time: %s)", name, cameraID, startTime.Format(time.RFC3339))

		u.wg.Add(1)
		go func(path, camID string, sTime time.Time) {
			defer func() {
				<-u.sem
				u.wg.Done()
			}()

			if err := u.processUpload(path, camID, sTime); err != nil {
				log.Printf("[uploader] Upload failed for %s: %v. Restoring for retry.", filepath.Base(path), err)
				// Rename back to original .mp4 for retry
				origPath := strings.TrimSuffix(path, ".upload")
				_ = os.Rename(path, origPath)
			} else {
				log.Printf("[uploader] Successfully uploaded and deleted segment: %s", filepath.Base(path))
			}
		}(uploadPath, cameraID, startTime)
	}
}

func (u *Uploader) processUpload(filePath string, cameraID string, startTime time.Time) error {
	// ── Step 1: Create the Interaction Session ───────────────────────
	interactionID, err := u.createInteraction(cameraID, startTime)
	if err != nil {
		return fmt.Errorf("failed to create interaction: %w", err)
	}

	// ── Step 2: Upload the Clip File ──────────────────────────────────
	err = u.uploadClip(interactionID, filePath)
	if err != nil {
		return fmt.Errorf("failed to upload clip file: %w", err)
	}

	// ── Step 3: Delete local file after success ───────────────────────
	if err := os.Remove(filePath); err != nil {
		log.Printf("[uploader] Warning: failed to delete uploaded file %s: %v", filePath, err)
	}

	return nil
}

func (u *Uploader) createInteraction(cameraID string, startTime time.Time) (string, error) {
	url := u.config.ServerURL + "/api/events/interaction"

	payload := map[string]interface{}{
		"camera_id":  cameraID,
		"start_time": startTime.Format(time.RFC3339),
		"status":     "completed", // Segments upload complete/historical clips
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-agent-api-key", u.config.APIKey)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result interactionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.ID, nil
}

func (u *Uploader) uploadClip(interactionID string, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Write metadata fields
	if err := writer.WriteField("interaction_id", interactionID); err != nil {
		return err
	}

	// Create file part with explicit Content-Type: video/mp4
	filename := filepath.Base(strings.TrimSuffix(filePath, ".upload"))
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="clip"; filename="%s"`, filename))
	h.Set("Content-Type", "video/mp4")
	part, err := writer.CreatePart(h)
	if err != nil {
		return err
	}

	if _, err := io.Copy(part, file); err != nil {
		return err
	}

	if err := writer.Close(); err != nil {
		return err
	}

	url := u.config.ServerURL + "/api/events/clip"
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("x-agent-api-key", u.config.APIKey)

	client := &http.Client{
		Timeout: 5 * time.Minute,
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
