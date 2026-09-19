package downloader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeShortsURL(t *testing.T) {
	for _, raw := range []string{
		"https://youtube.com/shorts/Y_yw1GkeDqc?si=lpQAmdH0vxrNYxAw",
		" youtube.com/shorts/Y_yw1GkeDqc ",
		"https://m.youtube.com/shorts/Y_yw1GkeDqc/?feature=share#test",
	} {
		got, err := normalizeShortsURL(raw)
		if err != nil || got != "https://www.youtube.com/watch?v=Y_yw1GkeDqc" {
			t.Errorf("normalizeShortsURL(%q) = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{
		"https://youtube.com.evil.test/shorts/Y_yw1GkeDqc",
		"https://youtube.com@evil.test/shorts/Y_yw1GkeDqc",
		"https://youtube.com/shorts/Y_yw1GkeDqcEXTRA",
		"https://youtube.com/shorts/short",
		"https://youtube.com/watch?v=Y_yw1GkeDqc",
		"ftp://youtube.com/shorts/Y_yw1GkeDqc",
	} {
		if _, err := normalizeShortsURL(raw); err == nil {
			t.Errorf("accepted invalid Shorts URL %q", raw)
		}
	}
}

func argValue(args []string, key string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func writeCompletedFixture(t *testing.T, args []string) string {
	t.Helper()
	path := strings.ReplaceAll(argValue(args, "--output"), "%(ext)s", "mp4")
	if err := os.WriteFile(path, make([]byte, 2048), 0600); err != nil {
		t.Fatal(err)
	}
	for i, arg := range args {
		if arg == "--print-to-file" {
			if err := os.WriteFile(args[i+2], []byte(path+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path
}

func TestYouTubeRetriesBotCheckWithoutCookies(t *testing.T) {
	t.Setenv("YTDLP_JS_RUNTIME", "deno")
	t.Setenv("YTDLP_PROXY", "http://operator:secret@proxy.test:8080")
	t.Setenv("YTDLP_POT_BASE_URL", "http://127.0.0.1:4416")
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "result.mp4")
	oldPath := filepath.Join(dir, "old.mp4")
	if err := os.WriteFile(oldPath, []byte("previous download"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := func(ctx context.Context, executable string, args []string) (string, error) {
		calls++
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--cookies") || !strings.Contains(joined, "--ignore-config") {
			t.Fatalf("unexpected cookie/config options: %v", args)
		}
		if argValue(args, "--proxy") != "http://operator:secret@proxy.test:8080" {
			t.Fatal("operator proxy was not forwarded")
		}
		if calls == 1 {
			return "ERROR: Sign in to confirm you're not a bot", errors.New("exit 1")
		}
		if !strings.Contains(joined, "youtube:player_client=mweb") || !strings.Contains(joined, "youtubepot-bgutilhttp:base_url=") {
			t.Fatal("token-provider retry was not configured")
		}
		writeCompletedFixture(t, args)
		return "", nil
	}
	path, err := downloadYouTube(context.Background(), "https://www.youtube.com/watch?v=Y_yw1GkeDqc", outputPath, runner)
	if err != nil || path != outputPath || calls != 2 {
		t.Fatalf("download = %q, %v; calls = %d", path, err, calls)
	}
	old, _ := os.ReadFile(oldPath)
	if string(old) != "previous download" {
		t.Fatal("previous download was modified")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 2 {
		t.Fatalf("attempt files were not cleaned up: %v", files)
	}
}

func TestPermanentYouTubeErrorsDoNotRetry(t *testing.T) {
	for _, message := range []string{"Private video", "Sign in to confirm your age", "Video unavailable", "File is larger than max-filesize"} {
		t.Run(message, func(t *testing.T) {
			calls := 0
			runner := func(context.Context, string, []string) (string, error) {
				calls++
				return message, errors.New("exit 1")
			}
			_, err := downloadYouTube(context.Background(), "https://youtube.com/watch?v=Y_yw1GkeDqc", filepath.Join(t.TempDir(), "result.mp4"), runner)
			if err == nil || calls != 1 {
				t.Fatalf("error = %v, calls = %d", err, calls)
			}
		})
	}
}

func TestFailedDownloadNeverReturnsOldOrPartialFile(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.mp4")
	os.WriteFile(oldPath, make([]byte, 2048), 0600)
	runner := func(_ context.Context, _ string, args []string) (string, error) {
		path := strings.ReplaceAll(argValue(args, "--output"), "%(ext)s", "mp4.part")
		os.WriteFile(path, make([]byte, 2048), 0600)
		return "", nil // An exit code alone is not proof of a completed download.
	}
	path, err := downloadYouTube(context.Background(), "https://youtube.com/watch?v=Y_yw1GkeDqc", filepath.Join(dir, "result.mp4"), runner)
	if err == nil || path != "" {
		t.Fatalf("returned an incomplete or unrelated video: %q, %v", path, err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 || files[0].Name() != "old.mp4" {
		t.Fatalf("incorrect cleanup: %v", files)
	}
}

func TestCompletedVideoValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		file string
		size int64
	}{
		{"empty", "video.mp4", 0},
		{"oversize merged file", "video.mp4", maxVideoBytes + 1},
		{"unfinished", "video.mp4.part", 2048},
		{"wrong container", "video.webm", 2048},
		{"outside attempt", "../old.mp4", 2048},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "attempt")
			os.Mkdir(dir, 0700)
			path := filepath.Join(dir, test.file)
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(test.size); err != nil {
				t.Fatal(err)
			}
			file.Close()
			manifest := filepath.Join(dir, "completed.txt")
			os.WriteFile(manifest, []byte(path+"\n"), 0600)
			if _, err := completedVideo(manifest, dir); err == nil {
				t.Fatalf("accepted invalid completed video: %s", test.name)
			}
		})
	}
}

func TestCancellationStopsRetriesAndCleansFiles(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	runner := func(_ context.Context, _ string, args []string) (string, error) {
		calls++
		writeCompletedFixture(t, args)
		cancel()
		return "", errors.New("killed")
	}
	_, err := downloadYouTube(ctx, "https://youtube.com/watch?v=Y_yw1GkeDqc", filepath.Join(dir, "result.mp4"), runner)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatalf("cancelled download left files: %v", files)
	}
}

// Opt in to actual downloads; regular tests need neither network nor yt-dlp.
func TestLiveDownloads(t *testing.T) {
	for _, test := range []struct {
		env      string
		download func(context.Context, string, int64) (string, error)
	}{
		{"TEST_YOUTUBE_SHORTS_URL", DownloadYouTubeVideo},
		{"TEST_TIKTOK_URL", DownloadTikTokVideo},
	} {
		t.Run(test.env, func(t *testing.T) {
			mediaURL := os.Getenv(test.env)
			if mediaURL == "" {
				t.Skip("set " + test.env + " to run a live download")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			path, err := test.download(ctx, mediaURL, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(path) })
			t.Logf("Downloaded %s", path)
		})
	}
}
