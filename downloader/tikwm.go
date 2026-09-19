package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// TikWM is an independent fallback for public TikTok videos when direct
// extraction is blocked. It receives the public link, never account cookies.
func tikWMDownload(ctx context.Context, mediaURL, outputPath string) (string, error) {
	client := &http.Client{Timeout: 35 * time.Second}
	urls, err := tikWMVideoURLs(ctx, client, "https://www.tikwm.com/api/", mediaURL)
	if err != nil {
		return "", err
	}
	for _, videoURL := range urls {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err = downloadTikTokMedia(ctx, client, videoURL, outputPath); err == nil {
			return outputPath, nil
		}
	}
	return "", err
}

func tikWMVideoURLs(ctx context.Context, client *http.Client, endpoint, mediaURL string) ([]string, error) {
	form := url.Values{"url": {mediaURL}, "hd": {"1"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", getUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TikWM returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code *int `json:"code"`
		Data struct {
			HDPlay string `json:"hdplay"`
			Play   string `json:"play"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid TikWM response: %w", err)
	}
	if result.Code == nil || *result.Code != 0 {
		return nil, fmt.Errorf("TikWM could not resolve this public video")
	}
	base, _ := url.Parse(endpoint)
	var urls []string
	for _, candidate := range []string{result.Data.HDPlay, result.Data.Play} {
		if candidate == "" {
			continue
		}
		u, err := url.Parse(candidate)
		if err != nil {
			continue
		}
		u = base.ResolveReference(u)
		if u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			continue
		}
		if len(urls) == 0 || urls[0] != u.String() {
			urls = append(urls, u.String())
		}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("TikWM returned no video (photo posts are not supported)")
	}
	return urls, nil
}

func downloadTikTokURL(ctx context.Context, mediaURL, outputPath string) (string, error) {
	client := &http.Client{Timeout: 35 * time.Second}
	if err := downloadTikTokMedia(ctx, client, mediaURL, outputPath); err != nil {
		return "", err
	}
	return outputPath, nil
}

func downloadTikTokMedia(ctx context.Context, client *http.Client, mediaURL, outputPath string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", getUserAgent())
	req.Header.Set("Referer", "https://www.tiktok.com/")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("TikTok media returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxVideoBytes {
		return fmt.Errorf("video exceeds Telegram's 50 MB size limit")
	}
	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(outputPath)
		}
	}()
	n, err := io.Copy(file, io.LimitReader(resp.Body, maxVideoBytes+1))
	if err != nil {
		return err
	}
	if n > maxVideoBytes {
		return fmt.Errorf("video exceeds Telegram's 50 MB size limit")
	}
	if n < 1024 {
		return fmt.Errorf("downloaded TikTok video is empty or incomplete")
	}
	// A large HTML error page is not a video, regardless of the HTTP status.
	var header [12]byte
	if _, err := file.ReadAt(header[:], 0); err != nil {
		return err
	}
	if string(header[4:8]) != "ftyp" {
		return fmt.Errorf("TikTok media response is not an MP4 video")
	}
	return nil
}
