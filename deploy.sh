#!/bin/bash

# Script to deploy VideoSaverBot as a systemd service on Ubuntu
# Author: memrook

set -e  # Exit immediately on any error

# Output colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
NC='\033[0m' # Reset color

# Formatted message printing
print_message() {
    echo -e "${GREEN}[VideoSaverBot]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

# Check for root/administrator privileges
if [ "$EUID" -ne 0 ]; then
    print_error "This script must be run with administrator privileges (sudo)."
    exit 1
fi

export PATH=$PATH:/usr/local/go/bin

# Installation parameters
APP_NAME="videosaverbot"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CURRENT_GIT_URL="$(git -C "$SCRIPT_DIR" config --get remote.origin.url 2>/dev/null || true)"
REPO_URL="${REPO_URL:-${CURRENT_GIT_URL:-https://github.com/memrook/VideoSaverBot.git}}"
INSTALL_DIR="/opt/$APP_NAME"
SERVICE_USER="videosaverbot"
CONFIG_DIR="/etc/$APP_NAME"
TOKEN_FILE="$CONFIG_DIR/token.conf"
CONCURRENT_DOWNLOADS=5
DEBUG_MODE="false"

# Ensure config and install directories exist immediately
mkdir -p "$CONFIG_DIR"
mkdir -p "$INSTALL_DIR"

print_message "Starting VideoSaverBot installation on the server..."

# Check for required utilities
print_message "Checking for required utilities..."
command -v git >/dev/null 2>&1 || { print_error "git is required. Installing..."; apt-get update && apt-get install -y git; }
command -v curl >/dev/null 2>&1 || { apt-get update && apt-get install -y curl; }

# Check and install Go
print_message "Checking for Go..."
if ! command -v go >/dev/null 2>&1; then
    print_warning "Go is not installed. Installing Go..."
    apt-get update
    apt-get install -y golang-go || apt-get install -y golang || true
    
    if ! command -v go >/dev/null 2>&1; then
        print_error "apt package not available. Installing official Go binary..."
        curl -fsSL https://go.dev/dl/go1.22.6.linux-amd64.tar.gz -o /tmp/go.tar.gz
        rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tar.gz
        rm -f /tmp/go.tar.gz
        echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
        chmod +x /etc/profile.d/go.sh
        export PATH=$PATH:/usr/local/go/bin
    fi
fi

# Install yt-dlp for downloading YouTube videos
print_message "Checking and installing yt-dlp..."
if ! command -v yt-dlp >/dev/null 2>&1; then
    print_warning "yt-dlp is not installed. Installing..."
    # Install pip if not present
    if ! command -v pip3 >/dev/null 2>&1; then
        apt-get install -y python3-pip
    fi
    
    # Install yt-dlp via pip
    pip3 install yt-dlp
    
    # Verify installation
    if command -v yt-dlp >/dev/null 2>&1; then
        YT_DLP_VERSION=$(yt-dlp --version)
        print_message "yt-dlp installed, version: $YT_DLP_VERSION"
    else
        print_error "Failed to install yt-dlp"
        exit 1
    fi
else
    YT_DLP_VERSION=$(yt-dlp --version)
    print_message "yt-dlp is already installed, version: $YT_DLP_VERSION"
fi

# Install ffmpeg for video processing (if needed)
print_message "Checking and installing ffmpeg..."
if ! command -v ffmpeg >/dev/null 2>&1; then
    print_warning "ffmpeg is not installed. Installing..."
    apt-get install -y ffmpeg
else
    print_message "ffmpeg is already installed"
fi

# Check Go version
GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
print_message "Installed Go version: $GO_VERSION"

# Create system user for the service
print_message "Creating system user for the service..."
id -u $SERVICE_USER >/dev/null 2>&1 || useradd --system --no-create-home $SERVICE_USER

# Create directories
print_message "Creating application directories..."
mkdir -p $INSTALL_DIR
mkdir -p $CONFIG_DIR

# Clone repository
print_message "Cloning repository from GitHub..."
if [ -d "$INSTALL_DIR/.git" ]; then
    print_warning "Repository already exists. Updating..."
    cd $INSTALL_DIR
    git pull
else
    git clone $REPO_URL $INSTALL_DIR
    cd $INSTALL_DIR
fi

# Install Go dependencies
print_message "Installing Go dependencies..."
cd $INSTALL_DIR
go mod tidy
go build -o $APP_NAME

# Create token configuration file
if [ ! -f "$TOKEN_FILE" ]; then
    print_message "Creating configuration file for Telegram token..."
    echo "# Telegram Bot Token" > $TOKEN_FILE
    echo "TELEGRAM_BOT_TOKEN=" >> $TOKEN_FILE
    print_warning "File $TOKEN_FILE created. Please add your bot token manually."
else
    print_message "Token configuration file already exists."
fi

# Create systemd service
print_message "Configuring systemd service..."
cat > /etc/systemd/system/$APP_NAME.service << EOF
[Unit]
Description=VideoSaverBot - Telegram bot for downloading videos
After=network.target

[Service]
Type=simple
User=$SERVICE_USER
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$TOKEN_FILE
ExecStart=$INSTALL_DIR/$APP_NAME -concurrent=$CONCURRENT_DOWNLOADS -debug=$DEBUG_MODE
Restart=always
RestartSec=10
StandardOutput=syslog
StandardError=syslog
SyslogIdentifier=$APP_NAME

[Install]
WantedBy=multi-user.target
EOF

# Set permissions
print_message "Setting permissions..."
chown -R $SERVICE_USER:$SERVICE_USER $INSTALL_DIR
chmod 750 $INSTALL_DIR
chmod 640 $TOKEN_FILE
chown root:$SERVICE_USER $TOKEN_FILE
chmod 750 $INSTALL_DIR/$APP_NAME

# Reload systemd and configure autostart
print_message "Configuring service autostart..."
systemctl daemon-reload
systemctl enable $APP_NAME.service

print_message "Installation completed!"
print_warning "Don't forget to add your Telegram bot token to $TOKEN_FILE"
print_message "Service management commands:"
print_message "  Start:                sudo systemctl start $APP_NAME"
print_message "  Stop:                 sudo systemctl stop $APP_NAME"
print_message "  Restart:              sudo systemctl restart $APP_NAME"
print_message "  Status:               sudo systemctl status $APP_NAME"
print_message "  View logs:            sudo journalctl -u $APP_NAME -f"

# Prompt to add token
read -p "Do you want to add the Telegram bot token now? (y/n): " ADD_TOKEN

if [ "$ADD_TOKEN" = "y" ] || [ "$ADD_TOKEN" = "Y" ]; then
    read -p "Enter Telegram bot token: " BOT_TOKEN
    sed -i "s/TELEGRAM_BOT_TOKEN=/TELEGRAM_BOT_TOKEN=$BOT_TOKEN/" $TOKEN_FILE
    print_message "Token added to configuration file."
    
    read -p "Start the service now? (y/n): " START_SERVICE
    if [ "$START_SERVICE" = "y" ] || [ "$START_SERVICE" = "Y" ]; then
        systemctl start $APP_NAME.service
        print_message "Service started! Check status: sudo systemctl status $APP_NAME"
    else
        print_message "You can start the service later with: sudo systemctl start $APP_NAME"
    fi
else
    print_message "Don't forget to add the token to $TOKEN_FILE before starting the bot!"
fi

exit 0 