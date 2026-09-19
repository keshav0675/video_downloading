package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTikWMResponse(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{"hd and sd", `{"code":0,"data":{"hdplay":"/hd.mp4","play":"/sd.mp4"}}`, 2},
		{"sd only", `{"code":0,"data":{"play":"/sd.mp4"}}`, 1},
		{"duplicates", `{"code":0,"data":{"hdplay":"/video.mp4","play":"/video.mp4"}}`, 1},
		{"missing status", `{"data":{"play":"/video.mp4"}}`, 0},
		{"rate limited", `{"code":-1,"msg":"rate limited"}`, 0},
		{"photo post", `{"code":0,"data":{"images":["/photo.jpg"]}}`, 0},
		{"bad scheme", `{"code":0,"data":{"play":"file:///tmp/video.mp4"}}`, 0},
		{"html", `<html>unavailable</html>`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.FormValue("url") != "https://www.tiktok.com/@user/video/123" || r.FormValue("hd") != "1" {
					t.Error("incorrect TikWM lookup request")
				}
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			urls, err := tikWMVideoURLs(context.Background(), server.Client(), server.URL+"/api/", "https://www.tiktok.com/@user/video/123")
			if (err != nil) != (test.want == 0) || len(urls) != test.want {
				t.Fatalf("URLs = %v, err = %v", urls, err)
			}
			for _, u := range urls {
				if !strings.HasPrefix(u, server.URL+"/") {
					t.Fatalf("relative media URL not resolved: %q", u)
				}
			}
		})
	}
}

func TestTikTokMediaValidation(t *testing.T) {
	video := make([]byte, 2048)
	copy(video[4:], "ftypisom")
	for _, test := range []struct {
		name string
		body []byte
		code int
		want bool
	}{
		{"mp4", video, 200, true},
		{"html error page", []byte(strings.Repeat("<html>error</html>", 1000)), 200, false},
		{"empty", nil, 200, false},
		{"forbidden", video, 403, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Referer") != "https://www.tiktok.com/" {
					t.Error("incorrect video referer")
				}
				w.WriteHeader(test.code)
				w.Write(test.body)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "video.mp4")
			err := downloadTikTokMedia(context.Background(), server.Client(), server.URL, path)
			if (err == nil) != test.want {
				t.Fatalf("download error = %v, want success = %v", err, test.want)
			}
			_, statErr := os.Stat(path)
			if (statErr == nil) != test.want {
				t.Fatal("invalid download was kept, or valid download was removed")
			}
		})
	}
}
