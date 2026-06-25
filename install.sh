#!/bin/bash
# ── Shinka Dynamics Agent Installer ───────────────────────────────────────────
# Mimics Tailscale's one-liner installer. Installs shinka-agent, configures it,
# verifies dependencies, and sets up a persistent systemd service.
#
# Usage:
#   sudo SHINKA_SERVER_URL="http://your-ip:3000" SHINKA_API_KEY="sk_..." ./install.sh
# ──────────────────────────────────────────────────────────────────────────────

set -euo pipefail

# Ensure script is run as root
if [ "$EUID" -ne 0 ]; then
  echo "Error: Please run this installer as root (using sudo)." >&2
  exit 1
fi

# Configuration inputs
SERVER_URL="${SHINKA_SERVER_URL:-}"
API_KEY="${SHINKA_API_KEY:-}"
AGENT_NAME="${SHINKA_AGENT_NAME:-$(hostname)}"

if [ -z "$SERVER_URL" ] || [ -z "$API_KEY" ]; then
  echo "Error: SHINKA_SERVER_URL and SHINKA_API_KEY environment variables are required." >&2
  echo "Example usage:" >&2
  echo "  sudo SHINKA_SERVER_URL=\"http://1.2.3.4:3000\" SHINKA_API_KEY=\"sk_yourkey\" ./install.sh" >&2
  exit 1
fi

echo "=== Starting Shinka Dynamics Agent Installer ==="

# 1. Check/Install System Dependencies
echo "-> Checking for FFmpeg..."
if ! command -v ffmpeg &> /dev/null; then
  echo "-> Installing FFmpeg..."
  if command -v apt-get &> /dev/null; then
    apt-get update && apt-get install -y ffmpeg
  elif command -v yum &> /dev/null; then
    yum install -y ffmpeg
  else
    echo "Warning: Could not auto-install FFmpeg. Please install it manually."
  fi
else
  echo "-> FFmpeg is already installed: $(ffmpeg -version | head -n 1)"
fi

# 2. Compile/Locate Binary
BINARY_SOURCE="shinka-agent"
if [ ! -f "$BINARY_SOURCE" ]; then
  echo "-> Compiling agent binary..."
  if command -v go &> /dev/null; then
    go build -o shinka-agent .
  else
    echo "Error: Pre-compiled binary not found and Go compiler (go) is missing." >&2
    echo "To cross-compile the agent for this machine on another system, run:" >&2
    echo "  GOOS=linux GOARCH=arm64 go build -o shinka-agent ." >&2
    echo "Then copy the 'shinka-agent' binary to this directory and run the installer again." >&2
    exit 1
  fi
fi

# 3. Create System User and Group
if ! id -u shinka-agent &>/dev/null; then
  echo "-> Creating shinka-agent system user and group..."
  groupadd --system shinka-agent
  useradd --system --gid shinka-agent --no-create-home --shell /usr/sbin/nologin --comment "Shinka Dynamics Camera Agent" shinka-agent
fi

# 4. Create Directories and Install Binary
echo "-> Installing binary to /usr/local/bin..."
cp "$BINARY_SOURCE" /usr/local/bin/shinka-agent
chmod 755 /usr/local/bin/shinka-agent

echo "-> Creating directories..."
mkdir -p /etc/shinka-agent
mkdir -p /var/log/shinka-agent
mkdir -p /var/lib/shinka-agent
mkdir -p /var/lib/shinka-agent/clips

# 5. Generate Configuration File
CONFIG_FILE="/etc/shinka-agent/agent.yml"
echo "-> Generating configuration at $CONFIG_FILE..."
cat <<EOF > "$CONFIG_FILE"
# Shinka Dynamics Agent Config
server_url: "$SERVER_URL"
api_key: "$API_KEY"
agent_name: "$AGENT_NAME"
state_dir: "/var/lib/shinka-agent"
clips_dir: "/var/lib/shinka-agent/clips"
max_clips_size_gb: 2.0
upload_concurrency: 2
EOF

# Secure configuration file permissions
chown -R root:shinka-agent /etc/shinka-agent
chmod 750 /etc/shinka-agent
chmod 640 "$CONFIG_FILE"

# Set ownership of writable paths to the agent user
chown -R shinka-agent:shinka-agent /var/log/shinka-agent
chown -R shinka-agent:shinka-agent /var/lib/shinka-agent
chmod 750 /var/log/shinka-agent
chmod 750 /var/lib/shinka-agent
chmod 750 /var/lib/shinka-agent/clips

# 6. Create Systemd Service
SERVICE_FILE="/etc/systemd/system/shinka-agent.service"
echo "-> Configuring Systemd service at $SERVICE_FILE..."
cat <<EOF > "$SERVICE_FILE"
[Unit]
Description=Shinka Dynamics Local Camera Agent
After=network.target

[Service]
Type=simple
User=shinka-agent
Group=shinka-agent
ExecStart=/usr/local/bin/shinka-agent -config /etc/shinka-agent/agent.yml
WorkingDirectory=/var/lib/shinka-agent
Restart=always
RestartSec=10
StandardOutput=append:/var/log/shinka-agent/output.log
StandardError=append:/var/log/shinka-agent/error.log

[Install]
WantedBy=multi-user.target
EOF

# 7. Enable and Start Service
echo "-> Starting systemd service..."
systemctl daemon-reload
systemctl enable shinka-agent.service
systemctl restart shinka-agent.service

echo "=== Installation Successful ==="
echo "Status: Running"
echo "Configuration: $CONFIG_FILE"
echo "Logs: tail -f /var/log/shinka-agent/output.log"
echo "----------------------------------------------------------"
echo "SD Card Wear Protection Tip:"
echo "To extend Raspberry Pi SD card lifetime, mount a RAM disk (tmpfs) on the clips folder:"
echo "  sudo mount -t tmpfs -o size=256M tmpfs /var/lib/shinka-agent/clips"
echo "To make it persistent, add this to /etc/fstab:"
echo "  tmpfs /var/lib/shinka-agent/clips tmpfs defaults,noatime,nosuid,nodev,noexec,mode=0750,uid=shinka-agent,gid=shinka-agent,size=256M 0 0"
echo "=========================================================="
