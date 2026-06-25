# Shinka Dynamics Local Camera Agent

The Shinka Dynamics Local Agent is a Go-based client binary deployed on-premise at customer sites (Linux, Windows, macOS). It connects directly to local IP camera streams (RTSP) and serves as the ingestion bridge for the central cloud platform.

---

## 1. What the Agent Does

The agent operates as a localized background service performing the following actions:

1. **RTSP Stream Relaying**
   * Spawns managed `ffmpeg` processes to connect to the IP cameras.
   * Relays the H.264 streams to the central `MediaMTX` WebRTC/HLS media server over a TCP connection for low-latency live viewing.

2. **Continuous Local Segment Recording**
   * Configures `ffmpeg` to capture and slice the incoming stream into **30-second local MP4 clips** (saved in a local `clips/` directory).
   * Stream copy mode (`-c copy`) is used to ensure direct video/audio passthrough, avoiding transcoding overhead and maintaining very low CPU usage.

3. **Status Reporting & Heartbeats**
   * Transmits a heartbeat payload every **10 seconds** to `/api/agents/heartbeat` including:
     * CPU/OS architecture.
     * Host system uptime.
     * Online/offline/error status of each camera.
   * Enables the cloud dashboard to detect and alert administrators when a camera or local box drops offline.

4. **Dynamic Configuration Syncing**
   * Requests assigned cameras and stream paths from `/api/agents/config` on startup and subsequent heartbeats.
   * Automatically starts, stops, or reconfigures stream relays when modifications are made in the cloud dashboard.

5. **Local Clip Uploading**
   * Runs an uploader worker every **5 seconds** scanning the `clips/` directory for finalized segments.
   * Hits the backend `/api/events/interaction` endpoint with its API key to provision an interaction session.
   * Performs a multipart upload of the `.mp4` video file to `/api/events/clip`.
   * Deletes the local clip file upon receiving a successful upload response.

---

## 2. Installation & Compilation

### Requirements
* **Go** (1.20+) if compiling from source.
* **FFmpeg** installed on the local system path.

### Compilation
Build the executable binary for the target architecture:

```bash
# Build for standard Linux 64-bit systems
GOOS=linux GOARCH=amd64 go build -o shinka-agent .

# Build for Windows 64-bit systems
GOOS=windows GOARCH=amd64 go build -o shinka-agent.exe .

# Build for Raspberry Pi (64-bit OS - Pi 4 / Pi 5 / Pi Zero 2 W)
GOOS=linux GOARCH=arm64 go build -o shinka-agent .

# Build for Raspberry Pi (32-bit legacy OS)
GOOS=linux GOARCH=arm go build -o shinka-agent .

# Build for Apple Silicon macOS (M1 / M2 / M3 / M4)
GOOS=darwin GOARCH=arm64 go build -o shinka-agent .

# Build for Intel-based macOS
GOOS=darwin GOARCH=amd64 go build -o shinka-agent .
```


---

## 3. Configuration Setup

Create an `agent.yml` file next to the compiled binary:

```yaml
# Cloud server endpoint URL
server_url: "http://your-dashboard-domain.com:3000"

# Agent API key (generated in Dashboard → Agents)
api_key: "sk_your_agent_api_key_here"

# Human-readable label for this agent instance
agent_name: "Office Agent 1"

# Optional: MediaMTX RTSP url (defaults to using server_url host if omitted)
# mediamtx_url: "rtsp://your-dashboard-domain.com"
```

---

## 4. Installation & Setup Methods

You can set up and execute the Shinka Dynamics Agent using either the **Automated Installer** or via **Manual Execution**.

### Method A: Automated Installer (Recommended for Linux & Raspberry Pi)

> [!NOTE]
> The automated installer is fully supported on Linux-based OS architectures, including standard AMD64 servers and **Raspberry Pi** (running Debian/Ubuntu or Raspberry Pi OS). It is not supported on macOS out-of-the-box.

To install the agent, configure its connectivity, resolve system dependencies (such as `ffmpeg`), and register it as an active systemd service in a single command, execute the installer script as root:

```bash
sudo SHINKA_SERVER_URL="http://your-dashboard-domain.com:3000" \
     SHINKA_API_KEY="sk_your_key_here" \
     SHINKA_AGENT_NAME="Office-Agent-1" \
     ./install.sh
```

The script will:
* Detect and install `ffmpeg` if not present.
* Copy the compiled `shinka-agent` binary to `/usr/local/bin/`.
* Write the configuration directly to `/etc/shinka-agent/agent.yml`.
* Install, enable, and start the systemd service.

**Managing the service:**
* View service logs: `tail -f /var/log/shinka-agent/output.log`
* Check service status: `sudo systemctl status shinka-agent`
* Stop the agent: `sudo systemctl stop shinka-agent`

---

### Method B: Manual Execution (Required for macOS)

If you prefer to run the agent manually in the foreground, or if you are running on **macOS**:

1. **Install FFmpeg**:
   * On Linux/Raspberry Pi: `sudo apt install ffmpeg`
   * On macOS: `brew install ffmpeg`

2. **Prepare Configuration**:
   ```bash
   cp agent.yml.example agent.yml
   # Edit values in agent.yml (server_url, api_key, agent_name)
   ```

3. **Start the Agent**:
   ```bash
   chmod +x shinka-agent
   ./shinka-agent -config agent.yml
   ```

4. **Background Persistence**:
   * **Linux/Raspberry Pi**: Write a systemd configuration pointing to your manual path.
   * **macOS**: Register a custom launchd plist agent under `~/Library/LaunchAgents/` to launch on user login.

---

## 5. Building Client Installer Packages

To deliver a self-contained installation package to your clients, use the configuration templates provided in the `package/` directory.

### A. Windows Installer (.exe Wizard)
We use **Inno Setup** (an open-source Windows installer compiler) to package the agent, static `ffmpeg.exe`, and install it automatically as a Windows background service.

1. Compile the agent for Windows:
   ```bash
   GOOS=windows GOARCH=amd64 go build -o shinka-agent.exe .
   ```
2. Download a static build of `ffmpeg.exe` and place it in the root of the `agent/` folder.
3. Open `package/windows/shinka-agent.iss` in the Inno Setup Compiler.
4. Click **Compile** to generate the self-contained setup wizard (`shinka-agent-setup.exe` in the `dist-installers/` folder).

This installer presents a visual setup wizard to your client, prompts for the Server URL and API Key, writes the configuration file, and registers the agent service.

### B. Debian/Ubuntu Package (.deb)
To create a standard `.deb` package containing systemd configurations for Debian-based OS architectures (including Ubuntu and Raspberry Pi OS):

1. Compile the agent binary for the client's architecture:
   ```bash
   GOOS=linux GOARCH=amd64 go build -o shinka-agent .
   ```
2. Copy files to the debian structure:
   ```bash
   mkdir -p package/deb/usr/local/bin
   cp shinka-agent package/deb/usr/local/bin/
   ```
3. Copy systemd config templates to `package/deb/etc/systemd/system/shinka-agent.service` if needed.
4. Run the Debian packager:
   ```bash
   dpkg-deb --build package/deb dist-installers/shinka-agent.deb
   ```

Clients can then install the agent with a single command:
```bash
sudo dpkg -i shinka-agent.deb
```



