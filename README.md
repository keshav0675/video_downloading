# VideoSaverBot for Telegram

Telegram bot in Go for downloading videos from popular social networks by URL.

## Features

- Video downloads from Instagram, Twitter/X, TikTok, Facebook, and YouTube Shorts
- Primary method: snapsave.app / snaptik.app with automatic decoding of obfuscated responses
- Fallback methods when the primary API is unavailable (DDInstagram, VXTwitter, tikmate.online)
- Correct video aspect ratios — ffprobe detects dimensions before sending to Telegram
- Concurrent download limiting with queue position feedback
- Single active request per user at a time
- Graceful shutdown — SIGTERM waits for active downloads to complete (up to 30s)
- 3-minute timeout for the entire download pipeline
- `/stats` command for the administrator
- Works in both direct messages and group chats
- Automatic cleanup of temporary files

## Supported Platforms

| Platform | Primary Method | Fallback Method |
|-----------|---------------|-----------------|
| Instagram | snapsave.app | DDInstagram |
| Twitter/X | twitterdownloader.snapsave.app | VXTwitter |
| TikTok | snaptik.app | tikmate.online |
| Facebook | snapsave.app | — |
| YouTube Shorts | yt-dlp | — |

## Installation

### Dependencies

- Go 1.21+
- `yt-dlp` — for YouTube Shorts (`apt install yt-dlp` or `pip install yt-dlp`)
- `ffprobe` (from ffmpeg package) — for video dimension detection (`apt install ffmpeg`)

### Local Build

```bash
git clone https://github.com/memrook/VideoSaverBot.git
cd VideoSaverBot
go mod tidy
go build -o videosaverbot
```

### Running

```bash
TELEGRAM_BOT_TOKEN="your_token" ./videosaverbot
# or
./videosaverbot -token="your_token" -debug=true -concurrent=10
```

Environment Variables:

| Variable | Description |
|-----------|-------------|
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (required) |
| `BOT_ADMIN_ID` | Telegram user ID of the administrator for the `/stats` command |

### Server Deployment (systemd)

```bash
chmod +x deploy.sh
sudo ./deploy.sh
```

The script will install dependencies, compile the binary, and create a systemd service and dedicated user.

Service Management:

```bash
systemctl start/stop/restart videosaverbot
systemctl status videosaverbot
journalctl -u videosaverbot -f
```

Service Configuration: `/etc/videosaverbot/token.conf`

```
TELEGRAM_BOT_TOKEN=...
BOT_ADMIN_ID=123456789
```

## Usage

1. Send the `/start` or `/help` command
2. Paste a video link from a supported platform
3. The bot will download and deliver the video

In group chats, the bot only responds to clean links, @mentions, or commands.

### Commands

| Command | Description |
|---------|-------------|
| `/start` | Welcome message |
| `/help` | Usage instructions |
| `/stats` | Bot statistics (for `BOT_ADMIN_ID` only) |

## Project Structure

```
main.go                    — entry point, routing, semaphore, graceful shutdown
downloader/downloader.go   — all download logic
go.mod / go.sum            — dependencies
deploy.sh                  — Ubuntu server deployment script
```

## License

MIT
