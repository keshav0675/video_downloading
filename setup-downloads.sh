#!/usr/bin/env bash
# Install/update the cookie-free download stack on Ubuntu 22.04+.
set -euo pipefail

if [ "$EUID" -ne 0 ]; then
    echo "Run this script with sudo."
    exit 1
fi

SCRIPT_DIR="$(dirname -- "${BASH_SOURCE[0]}")"
SCRIPT_DIR="$(cd -- "$SCRIPT_DIR" && pwd)"
TOOLS_DIR="/opt/videosaverbot-tools"
PROVIDER_VERSION="2.0.0"
PROVIDER_DIR="$TOOLS_DIR/bgutil-$PROVIDER_VERSION"
SERVICE_USER="videosaverbot"

apt-get update
apt-get install -y python3 python3-venv git ffmpeg build-essential \
    libcairo2-dev libpango1.0-dev libjpeg-dev libgif-dev librsvg2-dev
python3 -c 'import sys; assert sys.version_info >= (3, 10), "Python 3.10+ is required"'
id -u "$SERVICE_USER" >/dev/null 2>&1 || useradd --system --no-create-home "$SERVICE_USER"
mkdir -p "$TOOLS_DIR"
python3 -m venv "$TOOLS_DIR/venv"
"$TOOLS_DIR/venv/bin/python" -m pip install --upgrade pip
"$TOOLS_DIR/venv/bin/python" -m pip install --upgrade -r "$SCRIPT_DIR/download-requirements.txt"

# The Python plugin and the local token generator must use matching releases.
if [ ! -d "$PROVIDER_DIR" ]; then
    git clone --depth 1 --branch "$PROVIDER_VERSION" \
        https://github.com/Brainicism/bgutil-ytdlp-pot-provider.git "$PROVIDER_DIR"
fi
if [ "$(git -C "$PROVIDER_DIR" describe --tags --exact-match)" != "$PROVIDER_VERSION" ]; then
    echo "Unexpected token provider version in $PROVIDER_DIR"
    exit 1
fi

export PATH="$TOOLS_DIR/venv/bin:$PATH"
export DENO_DIR="$TOOLS_DIR/deno-cache"
cd "$PROVIDER_DIR/server"
deno install --allow-scripts=npm:canvas --frozen
# Keep installed code owned by root; only the runtime cache needs to be writable.
# This also lets subsequent root-run updates inspect the Git checkout safely.
chown -R "$SERVICE_USER:$SERVICE_USER" "$DENO_DIR"

# No login, browser profile, uploaded cookies, or public download API is used.
# The helper is only reachable on this server's loopback interface.
cat > /etc/systemd/system/videosaverbot-pot.service << EOF
[Unit]
Description=VideoSaverBot YouTube anonymous token helper
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
WorkingDirectory=$PROVIDER_DIR/server/node_modules
Environment="DENO_DIR=$DENO_DIR"
Environment="PATH=$TOOLS_DIR/venv/bin:/usr/local/bin:/usr/bin:/bin"
ExecStart=$TOOLS_DIR/venv/bin/deno run --allow-env --allow-net --allow-ffi=. --allow-read=. ../src/main.ts --host 127.0.0.1 --port 4416
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

# A drop-in also updates existing installations without replacing their token file.
mkdir -p /etc/systemd/system/videosaverbot.service.d
cat > /etc/systemd/system/videosaverbot.service.d/downloads.conf << EOF
[Unit]
Wants=videosaverbot-pot.service
After=videosaverbot-pot.service

[Service]
Environment="PATH=$TOOLS_DIR/venv/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
Environment="YTDLP_PATH=$TOOLS_DIR/venv/bin/yt-dlp"
Environment="YTDLP_JS_RUNTIME=deno:$TOOLS_DIR/venv/bin/deno"
Environment="DENO_DIR=$DENO_DIR"
EOF

systemctl daemon-reload
systemctl enable videosaverbot-pot.service
systemctl restart videosaverbot-pot.service
"$TOOLS_DIR/venv/bin/python" - << 'PY'
import time
import urllib.request

for attempt in range(10):
    try:
        with urllib.request.urlopen("http://127.0.0.1:4416/ping", timeout=2) as response:
            if response.status == 200:
                break
    except OSError:
        pass
    time.sleep(1)
else:
    raise SystemExit("Token helper did not start; check journalctl -u videosaverbot-pot")
PY
"$TOOLS_DIR/venv/bin/yt-dlp" --ignore-config --version
deno --version
echo "Download tools installed. Rebuild the bot and restart videosaverbot to apply code changes."
