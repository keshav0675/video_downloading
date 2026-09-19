# VideoSaverBot for Telegram

Telegram bot in Go for downloading videos from popular social networks by URL.

## Features

- Video downloads from Instagram, Twitter/X, TikTok, Facebook, and YouTube Shorts
- YouTube Shorts downloads without uploading cookies: current yt-dlp, JavaScript challenge support, and a local automatic PO-token helper
- TikTok downloads through yt-dlp, then TikWM and the existing website fallbacks
- Instagram/Facebook use snapsave.app; Twitter uses twitterdownloader.snapsave.app
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
| TikTok | yt-dlp with browser impersonation support | TikWM, then snaptik.app / tikmate.online |
| Facebook | snapsave.app | — |
| YouTube Shorts | yt-dlp default clients | Mobile web client with automatic PO tokens |

## Installation

### Dependencies

- Go 1.21+
- Python 3.10+ and current `yt-dlp[default,curl-cffi]` — for YouTube and direct TikTok downloads
- Deno 2.3+ — runs YouTube's JavaScript challenges
- `bgutil-ytdlp-pot-provider` plus its matching local token helper — automatically supplies anonymous YouTube PO tokens
- `ffmpeg` and `ffprobe` — combine audio/video, produce MP4, and detect dimensions

Use `download-requirements.txt` to install the Python tools together. Installing only an older distribution package of yt-dlp is insufficient.

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
| `YTDLP_PATH` | Optional full path to the yt-dlp executable (default: `yt-dlp` on PATH) |
| `YTDLP_JS_RUNTIME` | Optional runtime/path, e.g. `deno:/opt/videosaverbot-tools/venv/bin/deno`. Otherwise Deno is used, or Node on PATH if Deno is absent; Node must be 22+ |
| `YTDLP_POT_BASE_URL` | Optional token-helper URL (default used by the plugin: `http://127.0.0.1:4416`) |
| `YTDLP_PROXY` | Optional operator-supplied HTTP/SOCKS proxy for yt-dlp's YouTube and TikTok requests; use only if the server connection is blocked |

### Server Deployment (systemd)

```bash
chmod +x deploy.sh
sudo ./deploy.sh
```

Run this on Ubuntu 22.04+ from the updated source folder. The script installs/updates dependencies, builds this local source, and creates the bot service and a local `videosaverbot-pot` token-helper service. The helper listens only on `127.0.0.1:4416`.

To update an existing server, copy the updated source there and run:

```bash
sudo bash ./deploy.sh
sudo systemctl restart videosaverbot
```

The existing token configuration is preserved. To update just the download tools later:

```bash
sudo bash ./setup-downloads.sh
sudo systemctl restart videosaverbot
```

### Local download tools (Windows/macOS/Linux)

Create and activate a Python virtual environment, then install the dependencies:

```bash
python -m venv .venv
# Linux/macOS: source .venv/bin/activate
# Windows PowerShell: .\.venv\Scripts\Activate.ps1
python -m pip install --upgrade -r download-requirements.txt
```

Install ffmpeg (including ffprobe) and ensure it is on PATH. Run the bot from the activated environment so it can find yt-dlp and Deno.

For the local token helper, if Docker is already installed:

```bash
docker run -d --init --restart unless-stopped --name videosaverbot-pot \
  -p 127.0.0.1:4416:4416 brainicism/bgutil-ytdlp-pot-provider:2.0.0
```

Alternatively follow the provider's [native setup instructions](https://github.com/Brainicism/bgutil-ytdlp-pot-provider/tree/2.0.0). Keep the helper and Python plugin on the same version. Ubuntu deployment configures the native helper automatically.

### Cookie-free downloading and remaining restrictions

The bot does not read browser cookies or a cookies file. It ignores global yt-dlp configuration so account settings cannot be inherited accidentally. The helper generates temporary anonymous playback tokens automatically. YouTube first uses yt-dlp's current default clients, then retries with the mobile web client. TikTok tries direct extraction, TikWM, then the older website fallbacks. TikWM receives the public TikTok URL.

This supports public videos but cannot guarantee access from an IP address blocked by YouTube/TikTok or to private, members-only, age-restricted, deleted, or region-restricted content. A blocked server connection may require a working operator-supplied `YTDLP_PROXY`; merely changing the link or retrying indefinitely cannot guarantee a fix. No proxy is supplied or purchased by the bot.

The setup follows yt-dlp's [JavaScript runtime guide](https://github.com/yt-dlp/yt-dlp/wiki/EJS) and [PO-token guide](https://github.com/yt-dlp/yt-dlp/wiki/PO-Token-Guide). The [token provider](https://github.com/Brainicism/bgutil-ytdlp-pot-provider) also explains that tokens cannot guarantee removal of every bot check.

For diagnosis, check `journalctl -u videosaverbot -u videosaverbot-pot -f`. If dependencies are outdated, rerun `setup-downloads.sh`. Downloads must finish within three minutes and fit Telegram's 50 MB limit. Each yt-dlp attempt uses its own temporary directory; only its reported completed MP4 is returned, and incomplete files are removed.

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
downloader/downloader.go   — existing platform scrapers and HTTP downloads
downloader/ytdlp.go        — cookie-free YouTube/TikTok downloads, retries, output validation
downloader/tikwm.go        — TikTok API fallback and MP4 validation
go.mod / go.sum            — dependencies
deploy.sh                  — Ubuntu server deployment script
setup-downloads.sh         — download tools and local token-helper setup/update
download-requirements.txt  — compatible Python download dependencies
```

## Verification

```bash
go test ./...
go vet ./...
go build ./...
```

Tests cover shared/mobile links, retry decisions, cancelled/partial downloads, output isolation, file size, and TikWM responses. Live downloads are opt-in and require the download tools on PATH:

```bash
TEST_YOUTUBE_SHORTS_URL='https://youtube.com/shorts/VIDEO_ID' \
TEST_TIKTOK_URL='https://www.tiktok.com/@user/video/ID' \
go test ./downloader -run TestLiveDownloads -v -count=1
```

## License

MIT
