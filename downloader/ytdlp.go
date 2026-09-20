package downloader

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxVideoBytes int64 = 50 * 1024 * 1024

var shortsPath = regexp.MustCompile(`^/shorts/([a-zA-Z0-9_-]{11})/?$`)

// Normalize the mobile/share variants without passing tracking or playlist data.
func normalizeShortsURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Port() != "" {
		return "", fmt.Errorf("invalid YouTube Shorts URL")
	}
	switch strings.ToLower(u.Hostname()) {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
	default:
		return "", fmt.Errorf("invalid YouTube Shorts host")
	}
	match := shortsPath.FindStringSubmatch(u.Path)
	if match == nil {
		return "", fmt.Errorf("please send a youtube.com/shorts/VIDEO_ID link")
	}
	return "https://www.youtube.com/watch?v=" + match[1], nil
}

func ytDlpExecutable() string {
	if executable := strings.TrimSpace(os.Getenv("YTDLP_PATH")); executable != "" {
		return executable
	}
	return "yt-dlp"
}

func CheckYtDlpAvailability(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ytDlpExecutable(), "--ignore-config", "--version").Output()
	if err != nil {
		return fmt.Errorf("yt-dlp is unavailable; install the download dependencies described in README.md: %w", err)
	}
	log.Printf("yt-dlp version: %s", strings.TrimSpace(string(output)))
	return nil
}

type downloadFailure struct {
	message   string
	retryable bool
}

func (e *downloadFailure) Error() string { return e.message }

func retryableDownload(err error) bool {
	var failure *downloadFailure
	return errors.As(err, &failure) && failure.retryable
}

func classifyYtDlpFailure(output, platform string) error {
	lower := strings.ToLower(output)
	has := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(lower, part) {
				return true
			}
		}
		return false
	}
	switch {
	case has("larger than max-filesize", "exceeds size limit"):
		return &downloadFailure{"video exceeds Telegram's 50 MB size limit", false}
	case has("private video", "members-only", "join this channel", "login required", "requiring login"):
		return &downloadFailure{"this video requires an account and cannot be downloaded anonymously", false}
	case has("confirm your age", "age-restricted", "age restricted"):
		return &downloadFailure{"this video is age-restricted and cannot be downloaded anonymously", false}
	case has("not a bot", "sign in to confirm", "http error 429", "too many requests"):
		return &downloadFailure{platform + " is blocking anonymous requests from this server; the operator should check the download dependencies and server connection (YTDLP_PROXY is supported)", true}
	case has("video unavailable", "video is not available", "video has been removed", "video not available", "not available in your country", "copyright"):
		return &downloadFailure{"video is unavailable, removed, or restricted in the server's region", false}
	case has("no such option", "no supported javascript runtime", "javascript runtime", "challenge solver"):
		return &downloadFailure{"download dependencies need updating: install current yt-dlp, its default extras, and Deno (see README.md)", false}
	default:
		return &downloadFailure{platform + " download failed; check the server logs and update the download dependencies", true}
	}
}

func ytDlpArgs(mediaURL, template, manifest, playerClient string) []string {
	args := []string{
		"--ignore-config", // Never inherit browser-cookie or account settings.
		"--no-playlist", "--no-progress", "--no-colors", "--no-simulate",
		"--no-cache-dir", "--socket-timeout", "15",
		"--retries", "1", "--extractor-retries", "1", "--fragment-retries", "1",
		"--abort-on-unavailable-fragments", "--max-filesize", "50M",
		"--format", "bv[ext=mp4][vcodec^=avc1][height<=1080]+ba[ext=m4a]/b[ext=mp4][height<=1080]/bv[ext=mp4][height<=1080]+ba[ext=m4a]",
		"--merge-output-format", "mp4", "--remux-video", "mp4",
		"--output", template, "--print-to-file", "after_move:filepath", manifest,
	}
	runtime := strings.TrimSpace(os.Getenv("YTDLP_JS_RUNTIME"))
	if runtime != "" {
		args = append(args, "--js-runtimes", runtime)
	} else if _, err := exec.LookPath("deno"); err != nil {
		if _, err := exec.LookPath("node"); err == nil {
			args = append(args, "--js-runtimes", "node")
		}
	}
	if proxy := strings.TrimSpace(os.Getenv("YTDLP_PROXY")); proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	if playerClient != "" {
		extractorArgs := "youtube:player_client=" + playerClient
		if playerClient == "mweb" {
			// The default "auto" policy can skip the PLAYER token and only
			// request a GVS token after playback metadata succeeds. On a
			// challenged connection, request tokens for both stages.
			extractorArgs += ";fetch_pot=always"
		}
		args = append(args, "--extractor-args", extractorArgs)
	}
	if provider := strings.TrimSpace(os.Getenv("YTDLP_POT_BASE_URL")); provider != "" {
		args = append(args, "--extractor-args", "youtubepot-bgutilhttp:base_url="+provider)
	}
	return append(args, "--", mediaURL)
}

type ytDlpRunner func(context.Context, string, []string) (string, error)

func runYtDlp(ctx context.Context, executable string, args []string) (string, error) {
	output, err := exec.CommandContext(ctx, executable, args...).CombinedOutput()
	return string(output), err
}

// Only accept the exact, completed file reported by this invocation. A failed
// download must never return an older video from the user's directory.
func completedVideo(manifest, attemptDir string) (string, error) {
	data, err := os.ReadFile(manifest)
	if err != nil {
		return "", &downloadFailure{"downloader did not produce a completed video", true}
	}
	path := strings.TrimSpace(string(data))
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return "", fmt.Errorf("downloader returned an invalid output path")
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) != filepath.Clean(attemptDir) || strings.ToLower(filepath.Ext(path)) != ".mp4" {
		return "", fmt.Errorf("downloader returned an unexpected output file")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("completed video is missing or is not a regular file")
	}
	if info.Size() > maxVideoBytes {
		return "", &downloadFailure{"video exceeds Telegram's 50 MB size limit", false}
	}
	if info.Size() < 1024 {
		return "", &downloadFailure{"downloaded video is empty or incomplete", true}
	}
	return path, nil
}

func downloadYtDlpAttempt(ctx context.Context, mediaURL, outputPath, platform, playerClient string, runner ytDlpRunner) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	attemptDir, err := os.MkdirTemp(filepath.Dir(outputPath), ".ytdlp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(attemptDir)
	manifest := filepath.Join(attemptDir, "completed.txt")
	args := ytDlpArgs(mediaURL, filepath.Join(attemptDir, "video.%(ext)s"), manifest, playerClient)
	output, runErr := runner(ctx, ytDlpExecutable(), args)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if runErr != nil {
		var execErr *exec.Error
		if errors.As(runErr, &execErr) {
			return "", &downloadFailure{"yt-dlp is unavailable; install the download dependencies described in README.md", false}
		}
		// Credentials supplied in an operator's proxy URL must not enter logs.
		if proxy := os.Getenv("YTDLP_PROXY"); proxy != "" {
			output = strings.ReplaceAll(output, proxy, "[proxy]")
			if u, err := url.Parse(proxy); err == nil && u.User != nil {
				output = strings.ReplaceAll(output, u.User.String(), "[credentials]")
			}
		}
		log.Printf("%s yt-dlp attempt (%s): %v\n%s", platform, playerClient, runErr, output)
		return "", classifyYtDlpFailure(output, platform)
	}
	// yt-dlp may exit successfully when it skips an oversized video.
	if strings.Contains(strings.ToLower(output), "larger than max-filesize") {
		return "", classifyYtDlpFailure(output, platform)
	}
	path, err := completedVideo(manifest, attemptDir)
	if err != nil {
		return "", err
	}
	if err := os.Rename(path, outputPath); err != nil {
		return "", fmt.Errorf("failed to save completed video: %w", err)
	}
	return outputPath, nil
}

func downloadYouTube(ctx context.Context, mediaURL, outputPath string, runner ytDlpRunner) (string, error) {
	var lastErr error
	// Let current yt-dlp choose its defaults, then try the mobile web client
	// with player and media tokens supplied by the installed bgutil provider.
	for _, client := range []string{"", "mweb"} {
		attemptCtx, cancel := context.WithTimeout(ctx, 80*time.Second)
		path, err := downloadYtDlpAttempt(attemptCtx, mediaURL, outputPath, "YouTube", client, runner)
		cancel()
		if err == nil {
			return path, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		lastErr = err
		if !retryableDownload(err) && !errors.Is(err, context.DeadlineExceeded) {
			break
		}
	}
	return "", lastErr
}

func DownloadYouTubeVideo(ctx context.Context, mediaURL string, userID int64) (string, error) {
	mediaURL, err := normalizeShortsURL(mediaURL)
	if err != nil {
		return "", err
	}
	outputPath, err := createUserDirectory(userID, "youtube")
	if err != nil {
		return "", err
	}
	return downloadYouTube(ctx, mediaURL, outputPath, runYtDlp)
}

func DownloadTikTokVideo(ctx context.Context, mediaURL string, userID int64) (string, error) {
	outputPath, err := createUserDirectory(userID, "tiktok")
	if err != nil {
		return "", err
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
	path, primaryErr := downloadYtDlpAttempt(attemptCtx, mediaURL, outputPath, "TikTok", "", runYtDlp)
	cancel()
	if primaryErr == nil {
		return path, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	log.Printf("TikTok direct download failed, trying TikWM: %v", primaryErr)
	fallbackCtx, fallbackCancel := context.WithTimeout(ctx, 70*time.Second)
	path, err = tikWMDownload(fallbackCtx, mediaURL, outputPath)
	fallbackCancel()
	if err == nil {
		return path, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	log.Printf("TikWM failed, trying website fallback: %v", err)
	path, err = snapsaveDownload(ctx, mediaURL, userID)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		log.Printf("TikTok website fallback failed: %v", err)
		return "", fmt.Errorf("TikTok download failed using all available methods: %w", primaryErr)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxVideoBytes {
		os.Remove(path)
		return "", &downloadFailure{"video exceeds Telegram's 50 MB size limit", false}
	}
	return path, nil
}
