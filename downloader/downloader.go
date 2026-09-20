package downloader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
)

var tempDirMutex = &sync.Mutex{}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type SnapsaveResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Description string `json:"description,omitempty"`
		Preview     string `json:"preview,omitempty"`
		Media       []struct {
			URL        string `json:"url"`
			Thumbnail  string `json:"thumbnail,omitempty"`
			Type       string `json:"type"`
			Resolution string `json:"resolution,omitempty"`
		} `json:"media"`
	} `json:"data"`
}

type PlatformType string

const (
	Instagram PlatformType = "instagram"
	Twitter   PlatformType = "twitter"
	TikTok    PlatformType = "tiktok"
	Facebook  PlatformType = "facebook"
)

// decodeSnapApp decrypts data according to the snapsave algorithm
func decodeSnapApp(args []string) string {
	if len(args) < 6 {
		return ""
	}

	h, u, n, t, e, r := args[0], args[1], args[2], args[3], args[4], args[5]
	_ = u
	_ = r

	tNum, err := strconv.Atoi(t)
	if err != nil {
		return ""
	}

	eNum, err := strconv.Atoi(e)
	if err != nil || eNum < 2 || eNum >= len(n) || eNum > 64 {
		return ""
	}

	decode := func(d string, e, f int) string {
		g := strings.Split("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ+/", "")
		hArr := g[:e]
		iArr := g[:f]

		dChars := strings.Split(d, "")
		j := 0
		for c := 0; c < len(dChars); c++ {
			b := dChars[len(dChars)-1-c]
			idx := -1
			for i, char := range hArr {
				if char == b {
					idx = i
					break
				}
			}
			if idx != -1 {
				j += idx * int(math.Pow(float64(e), float64(c)))
			}
		}

		k := ""
		for j > 0 {
			k = iArr[j%f] + k
			j = int(math.Floor(float64(j) / float64(f)))
		}

		if k == "" {
			return "0"
		}
		return k
	}

	result := ""
	hLen := len(h)

	for i := 0; i < hLen; {
		s := ""
		for i < hLen && string(h[i]) != string(n[eNum]) {
			s += string(h[i])
			i++
		}
		i++

		for j := 0; j < len(n); j++ {
			s = strings.ReplaceAll(s, string(n[j]), strconv.Itoa(j))
		}

		decoded := decode(s, eNum, 10)
		decodedNum, err := strconv.Atoi(decoded)
		if err != nil {
			continue
		}
		charCode := decodedNum - tNum
		if charCode >= 0 && charCode <= 1114111 {
			result += string(rune(charCode))
		}
	}

	return fixEncoding(result)
}

func fixEncoding(str string) string {

	if utf8.ValidString(str) {
		return str
	}

	chars := []rune(str)
	bytes := make([]byte, 0, len(chars))

	for _, char := range chars {
		charCode := int(char)
		if charCode >= 0 && charCode <= 255 {
			bytes = append(bytes, byte(charCode))
		}
	}

	if utf8.Valid(bytes) {
		return string(bytes)
	}

	return str
}

// getEncodedSnapApp extracts encoded parameters from snapsave data
func getEncodedSnapApp(data string) []string {
	parts := strings.Split(data, "decodeURIComponent(escape(r))}(")
	if len(parts) < 2 {

		if strings.Contains(data, "_0xe98c") {
			return tryExtractObfuscatedParams(data)
		}

		return nil
	}

	innerParts := strings.Split(parts[1], "))")
	if len(innerParts) < 1 {
		return nil
	}

	encoded := innerParts[0]

	commaParts := strings.Split(encoded, ",")
	result := make([]string, 0, len(commaParts))

	for _, part := range commaParts {
		cleaned := strings.ReplaceAll(strings.TrimSpace(part), "\"", "")
		result = append(result, cleaned)
	}

	return result
}

func tryExtractObfuscatedParams(data string) []string {
	return nil
}

func getDecodedSnapSave(data string) string {
	parts := strings.Split(data, "getElementById(\"download-section\").innerHTML = \"")
	if len(parts) < 2 {
		return ""
	}

	innerParts := strings.Split(parts[1], "\"; document.getElementById(\"inputData\").remove(); ")
	if len(innerParts) < 1 {
		return ""
	}

	result := innerParts[0]

	result = strings.ReplaceAll(result, "\\\\", "")
	result = strings.ReplaceAll(result, "\\", "")

	return result
}

func decryptSnapSave(data string) string {
	encoded := getEncodedSnapApp(data)
	if encoded == nil {
		return ""
	}
	decoded := decodeSnapApp(encoded)
	return getDecodedSnapSave(decoded)
}

func getDecodedSnaptik(data string) string {
	parts := strings.Split(data, "$(\"#download\").innerHTML = \"")
	if len(parts) < 2 {
		return ""
	}

	innerParts := strings.Split(parts[1], "\"; document.getElementById(\"inputData\").remove(); ")
	if len(innerParts) < 1 {
		return ""
	}

	result := innerParts[0]

	result = strings.ReplaceAll(result, "\\\\", "")
	result = strings.ReplaceAll(result, "\\", "")

	return result
}

func decryptSnaptik(data string) string {
	encoded := getEncodedSnapApp(data)
	if encoded == nil {
		return ""
	}
	decoded := decodeSnapApp(encoded)
	return getDecodedSnaptik(decoded)
}

func normalizeURL(url string) string {
	twitterRegex := regexp.MustCompile(`^https://(?:x|twitter)\.com(?:/(?:i/web|[^/]+)/status/(\d+)(?:.*)?)?$`)
	if twitterRegex.MatchString(url) {
		return url
	}

	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		if !strings.Contains(url, "://www.") {
			re := regexp.MustCompile(`^(https?://)([^./]+\.[^./]+)(\/.*)?$`)
			if re.MatchString(url) {
				return re.ReplaceAllString(url, "$1www.$2$3")
			}
		}
	}

	return url
}

func fixThumbnail(url string) string {
	toReplace := "https://snapinsta.app/photo.php?photo="
	if strings.Contains(url, toReplace) {
		decoded, err := neturl.QueryUnescape(strings.Replace(url, toReplace, "", 1))
		if err != nil {
			return url
		}
		return decoded
	}
	return url
}

func getUserAgent() string {
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
}

func generateUniqueID() string {
	randomBytes := make([]byte, 8)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Unix()%10000)
	}
	return hex.EncodeToString(randomBytes)
}

func detectPlatform(mediaURL string) PlatformType {
	lowerURL := strings.ToLower(mediaURL)
	switch {
	case strings.Contains(lowerURL, "instagram.com"):
		return Instagram
	case strings.Contains(lowerURL, "twitter.com") || strings.Contains(lowerURL, "x.com"):
		return Twitter
	case strings.Contains(lowerURL, "tiktok.com"):
		return TikTok
	case strings.Contains(lowerURL, "facebook.com") || strings.Contains(lowerURL, "fb.watch"):
		return Facebook
	default:
		return Instagram
	}
}

func snapsaveDownload(ctx context.Context, mediaURL string, userID int64) (string, error) {
	platform := detectPlatform(mediaURL)
	primaryCtx := ctx
	if platform == Facebook {
		// Leave time for the direct fallback within the bot's three-minute limit.
		var cancel context.CancelFunc
		primaryCtx, cancel = context.WithTimeout(ctx, 70*time.Second)
		defer cancel()
	}

	outputPath, err := createUserDirectory(userID, string(platform))
	if err != nil {
		return "", err
	}

	videoURL, err := getSnapsaveVideoURL(primaryCtx, mediaURL)
	if err != nil {
		if platform == Facebook {
			log.Printf("Facebook SnapSave lookup failed, trying yt-dlp: %v", err)
		}
		return fallbackDownload(ctx, mediaURL, userID, platform)
	}

	var result string
	if platform == TikTok {
		result, err = downloadTikTokURL(ctx, videoURL, outputPath)
	} else {
		result, err = downloadMedia(primaryCtx, videoURL, outputPath)
	}
	if err != nil {
		if platform == Facebook {
			os.Remove(outputPath)
			log.Printf("Facebook SnapSave media failed, trying yt-dlp: %v", err)
		}
		return fallbackDownload(ctx, mediaURL, userID, platform)
	}
	return result, nil
}

func getSnapsaveVideoURL(ctx context.Context, mediaURL string) (string, error) {
	platform := detectPlatform(mediaURL)

	switch platform {
	case TikTok:
		return getSnapsaveVideoURLTikTok(ctx, mediaURL)
	case Twitter:
		return getSnapsaveVideoURLTwitter(ctx, mediaURL)
	case Instagram, Facebook:
		return getSnapsaveVideoURLInstagramFacebook(ctx, mediaURL)
	default:
		return "", fmt.Errorf("unsupported platform: %s", platform)
	}
}

func getSnapsaveVideoURLTikTok(ctx context.Context, mediaURL string) (string, error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
	}

	homeReq, err := http.NewRequestWithContext(ctx, "GET", "https://snaptik.app/", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request to snaptik.app: %v", err)
	}

	homeReq.Header.Set("User-Agent", getUserAgent())

	homeResp, err := client.Do(homeReq)
	if err != nil {
		return "", fmt.Errorf("failed to execute request to snaptik.app: %v", err)
	}
	defer homeResp.Body.Close()

	if homeResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("invalid status code from snaptik.app: %d", homeResp.StatusCode)
	}

	homeDoc, err := goquery.NewDocumentFromReader(homeResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML from snaptik.app: %v", err)
	}

	token, exists := homeDoc.Find("input[name='token']").Attr("value")
	if !exists || token == "" {
		return "", fmt.Errorf("token not found on snaptik.app page")
	}

	formData := neturl.Values{}
	formData.Set("url", mediaURL)
	formData.Set("token", token)

	postReq, err := http.NewRequestWithContext(ctx, "POST", "https://snaptik.app/abc2.php", strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create POST request to snaptik.app: %v", err)
	}

	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("Accept", "*/*")
	postReq.Header.Set("Origin", "https://snaptik.app")
	postReq.Header.Set("Referer", "https://snaptik.app/")
	postReq.Header.Set("User-Agent", getUserAgent())

	postResp, err := client.Do(postReq)
	if err != nil {
		return "", fmt.Errorf("failed to execute POST request to snaptik.app: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("invalid status code from abc2.php: %d", postResp.StatusCode)
	}

	body, err := io.ReadAll(postResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response from snaptik.app: %v", err)
	}

	decryptedHTML := decryptSnaptik(string(body))
	if decryptedHTML == "" {
		return "", fmt.Errorf("failed to decrypt snaptik data")
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(decryptedHTML))
	if err != nil {
		return "", fmt.Errorf("failed to parse decrypted HTML from snaptik: %v", err)
	}

	videoURL, exists := doc.Find(".download-box > .video-links > a").Attr("href")
	if !exists || videoURL == "" {
		videoURL, exists = doc.Find("a[download]").Attr("href")
		if !exists || videoURL == "" {
			videoURL, exists = doc.Find("a[href*='.mp4']").Attr("href")
			if !exists || videoURL == "" {
				return "", fmt.Errorf("video URL not found in snaptik response")
			}
		}
	}

	return videoURL, nil
}

func getSnapsaveVideoURLTwitter(ctx context.Context, mediaURL string) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	homeReq, err := http.NewRequestWithContext(ctx, "GET", "https://twitterdownloader.snapsave.app/", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request to twitterdownloader.snapsave.app: %v", err)
	}

	homeReq.Header.Set("User-Agent", getUserAgent())

	homeResp, err := client.Do(homeReq)
	if err != nil {
		return "", fmt.Errorf("failed to execute request to twitterdownloader.snapsave.app: %v", err)
	}
	defer homeResp.Body.Close()

	if homeResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("invalid status code from twitterdownloader.snapsave.app: %d", homeResp.StatusCode)
	}

	homeDoc, err := goquery.NewDocumentFromReader(homeResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML from twitterdownloader.snapsave.app: %v", err)
	}

	token, exists := homeDoc.Find("input[name='token']").Attr("value")
	if !exists || token == "" {
		return "", fmt.Errorf("token not found on twitterdownloader.snapsave.app page")
	}

	formData := neturl.Values{}
	formData.Set("url", mediaURL)
	formData.Set("token", token)

	postReq, err := http.NewRequestWithContext(ctx, "POST", "https://twitterdownloader.snapsave.app/action.php", strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create POST request to twitterdownloader.snapsave.app: %v", err)
	}

	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("Accept", "*/*")
	postReq.Header.Set("Origin", "https://twitterdownloader.snapsave.app")
	postReq.Header.Set("Referer", "https://twitterdownloader.snapsave.app/")
	postReq.Header.Set("User-Agent", getUserAgent())

	postResp, err := client.Do(postReq)
	if err != nil {
		return "", fmt.Errorf("failed to execute POST request to twitterdownloader.snapsave.app: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("invalid status code from action.php: %d", postResp.StatusCode)
	}

	body, err := io.ReadAll(postResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response from twitterdownloader.snapsave.app: %v", err)
	}

	var jsonResponse struct {
		Data string `json:"data"`
	}

	if err := json.Unmarshal(body, &jsonResponse); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %v", err)
	}

	if jsonResponse.Data == "" {
		return "", fmt.Errorf("empty data in JSON response")
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(jsonResponse.Data))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML from JSON: %v", err)
	}

	videoURL, exists := doc.Find("#download-block > .abuttons > a").Attr("href")
	if !exists || videoURL == "" {
		return "", fmt.Errorf("video URL not found in twitterdownloader response")
	}

	return videoURL, nil
}

// getSnapsaveVideoURLInstagramFacebook retrieves video URL for Instagram and Facebook
func getSnapsaveVideoURLInstagramFacebook(ctx context.Context, mediaURL string) (string, error) {
	apiURL := "https://snapsave.app/action.php?lang=en"

	formData := neturl.Values{}
	formData.Set("url", normalizeURL(mediaURL))

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", getUserAgent())
	req.Header.Set("Referer", "https://snapsave.app/")
	req.Header.Set("Origin", "https://snapsave.app")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("invalid status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %v", err)
	}

	decryptedHTML := decryptSnapSave(string(body))
	if decryptedHTML == "" {
		return findVideoURLWithRegex(string(body))
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(decryptedHTML))
	if err != nil {
		return "", fmt.Errorf("failed to parse decrypted HTML: %v", err)
	}

	var videoURL string

	if doc.Find("table.table").Length() > 0 {
		doc.Find("tbody > tr").Each(func(i int, s *goquery.Selection) {
			td := s.Find("td")
			if td.Length() >= 3 {
				href, exists := td.Eq(2).Find("a").Attr("href")
				if exists && href != "" && videoURL == "" {
					videoURL = href
				} else {
					onclick, exists := td.Eq(2).Find("button").Attr("onclick")
					if exists && strings.Contains(onclick, "get_progressApi") {
						re := regexp.MustCompile(`get_progressApi\('([^']+)'\)`)
						matches := re.FindStringSubmatch(onclick)
						if len(matches) > 1 && videoURL == "" {
							videoURL = "https://snapsave.app" + matches[1]
						}
					}
				}
			}
		})
	}

	if videoURL == "" && doc.Find("div.card").Length() > 0 {
		doc.Find("div.card").Each(func(i int, s *goquery.Selection) {
			cardBody := s.Find("div.card-body")
			href, exists := cardBody.Find("a").Attr("href")
			if exists && href != "" && videoURL == "" {
				videoURL = href
			}
		})
	}

	if videoURL == "" && doc.Find("div.download-items").Length() > 0 {
		doc.Find("div.download-items").Each(func(i int, s *goquery.Selection) {
			itemBtn := s.Find("div.download-items__btn")
			href, exists := itemBtn.Find("a").Attr("href")
			if exists && href != "" && videoURL == "" {
				videoURL = href
			}
		})
	}

	if videoURL == "" {
		href, exists := doc.Find("a").Attr("href")
		if exists && href != "" {
			videoURL = href
		}
	}

	if videoURL == "" {
		return "", fmt.Errorf("failed to find video URL in decrypted HTML")
	}

	return videoURL, nil
}

func findVideoURLWithRegex(htmlContent string) (string, error) {
	videoPatterns := []string{
		`href="([^"]*\.mp4[^"]*)"`,
		`data-href="([^"]*\.mp4[^"]*)"`,
		`onclick="[^"]*get_progressApi\('([^']+)'\)"`,
	}

	for _, pattern := range videoPatterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(htmlContent)
		if len(matches) > 1 {
			videoURL := matches[1]
			if strings.Contains(pattern, "get_progressApi") {
				videoURL = "https://snapsave.app" + videoURL
			}
			return videoURL, nil
		}
	}

	return "", fmt.Errorf("failed to find video URL via regular expressions")
}

func createUserDirectory(userID int64, platform string) (string, error) {
	workDir, err := os.Getwd()
	if err != nil {
		workDir = "."
	}

	tempDirBase := filepath.Join(workDir, "temp_videos")

	tempDirMutex.Lock()
	defer tempDirMutex.Unlock()

	if err := os.MkdirAll(tempDirBase, 0755); err != nil {
		return "", fmt.Errorf("failed to create base directory for temporary files: %v", err)
	}

	userDir := filepath.Join(tempDirBase, strconv.FormatInt(userID, 10))
	if err := os.MkdirAll(userDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create user directory for temporary files: %v", err)
	}

	cacheDir := filepath.Join(tempDirBase, ".cache")
	configDir := filepath.Join(tempDirBase, ".config")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		fmt.Printf("Warning: failed to create cache directory: %v\n", err)
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		fmt.Printf("Warning: failed to create config directory: %v\n", err)
	}

	uniqueID := generateUniqueID()
	timestamp := time.Now().UnixNano()

	outputPath := filepath.Join(userDir, fmt.Sprintf("%s_%d_%s_%d.mp4", platform, userID, uniqueID, timestamp))
	return outputPath, nil
}

// fallbackDownload handles video downloads via fallback methods
func fallbackDownload(ctx context.Context, mediaURL string, userID int64, platform PlatformType) (string, error) {
	switch platform {
	case Instagram:
		return fallbackInstagramDownload(ctx, mediaURL, userID)
	case Twitter:
		return fallbackTwitterDownload(ctx, mediaURL, userID)
	case TikTok:
		return fallbackTikTokDownload(ctx, mediaURL, userID)
	case Facebook:
		return fallbackFacebookDownload(ctx, mediaURL, userID)
	default:
		return "", fmt.Errorf("platform %s is not supported in fallback mode", platform)
	}
}

func DownloadInstagramVideo(ctx context.Context, url string, userID int64) (string, error) {
	return snapsaveDownload(ctx, url, userID)
}

func DownloadTwitterVideo(ctx context.Context, url string, userID int64) (string, error) {
	return snapsaveDownload(ctx, url, userID)
}

func DownloadFacebookVideo(ctx context.Context, url string, userID int64) (string, error) {
	return snapsaveDownload(ctx, url, userID)
}

// fallbackInstagramDownload fallback method for Instagram
func fallbackInstagramDownload(ctx context.Context, url string, userID int64) (string, error) {
	outputPath, err := createUserDirectory(userID, "instagram")
	if err != nil {
		return "", err
	}

	// Replace instagram.com with ddinstagram.com for easy video extraction
	ddUrl := strings.Replace(url, "instagram.com", "ddinstagram.com", 1)

	// Configure HTTP client with extended timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	// Send request to ddinstagram to obtain HTML page
	req, err := http.NewRequestWithContext(ctx, "GET", ddUrl, nil)
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	// Set headers to mimic TelegramBot
	req.Header.Set("User-Agent", "TelegramBot (like InstagramBot)")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error requesting ddinstagram: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("received invalid status code: %d", resp.StatusCode)
	}

	// Search for video URL via regular expressions in HTML
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	// Patterns for finding video URL
	videoPatterns := []string{
		`"video_url":"([^"]+)"`,
		`og:video" content="([^"]+)"`,
		`twitter:player:stream" content="([^"]+)"`,
		`href="([^"]*\.mp4[^"]*)"`,
	}

	var videoURL string
	for _, pattern := range videoPatterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(string(body))
		if len(matches) > 1 {
			videoURL = matches[1]
			// Decode escape sequences
			videoURL = strings.ReplaceAll(videoURL, "\\u0026", "&")
			videoURL = strings.ReplaceAll(videoURL, "\\/", "/")
			break
		}
	}

	if videoURL == "" {
		return "", fmt.Errorf("failed to find video URL in fallback mode for Instagram")
	}

	return downloadMedia(ctx, videoURL, outputPath)
}

// fallbackTwitterDownload fallback method for Twitter
func fallbackTwitterDownload(ctx context.Context, url string, userID int64) (string, error) {
	outputPath, err := createUserDirectory(userID, "twitter")
	if err != nil {
		return "", err
	}

	// Replace x.com with twitter.com, then twitter.com with vxtwitter.com
	url = strings.Replace(url, "x.com", "twitter.com", 1)
	vxUrl := strings.Replace(url, "twitter.com", "vxtwitter.com", 1)

	// Configure HTTP client
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	// Send request to vxTwitter
	req, err := http.NewRequestWithContext(ctx, "GET", vxUrl, nil)
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("User-Agent", "TelegramBot (like TwitterBot)")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error requesting vxTwitter: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("received invalid status code: %d", resp.StatusCode)
	}

	// Search for video URL via regular expressions in HTML
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	// Patterns for finding video URL in Twitter
	videoPatterns := []string{
		`twitter:player:stream" content="([^"]+)"`,
		`og:video" content="([^"]+)"`,
		`twitter:video" content="([^"]+)"`,
		`"video_url":"([^"]+)"`,
	}

	var videoURL string
	for _, pattern := range videoPatterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(string(body))
		if len(matches) > 1 {
			videoURL = matches[1]
			// Decode escape sequences
			videoURL = strings.ReplaceAll(videoURL, "\\u0026", "&")
			videoURL = strings.ReplaceAll(videoURL, "\\/", "/")
			break
		}
	}

	if videoURL == "" {
		return "", fmt.Errorf("failed to find video URL in fallback mode for Twitter")
	}

	return downloadMedia(ctx, videoURL, outputPath)
}

// fallbackTikTokDownload fallback method for TikTok via tikmate.online
func fallbackTikTokDownload(ctx context.Context, url string, userID int64) (string, error) {

	// Create output directory
	outputPath, err := createUserDirectory(userID, "tiktok")
	if err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Send POST request to tikmate.online API
	formData := neturl.Values{}
	formData.Set("url", url)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://tikmate.online/download", strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request to tikmate.online: %v", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", getUserAgent())
	req.Header.Set("Origin", "https://tikmate.online")
	req.Header.Set("Referer", "https://tikmate.online/")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request to tikmate.online: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("invalid status code from tikmate.online: %d", resp.StatusCode)
	}

	// Read response as JSON
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response from tikmate.online: %v", err)
	}

	// Parse JSON response
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			VideoURL string `json:"play"`
		} `json:"data"`
	}

	err = json.Unmarshal(body, &response)
	if err != nil {
		// If JSON parsing fails, try extracting URL via regular expressions
		return fallbackTikTokRegexExtract(ctx, string(body), outputPath)
	}

	if !response.Success || response.Data.VideoURL == "" {
		return "", fmt.Errorf("tikmate.online could not process URL")
	}

	return downloadTikTokURL(ctx, response.Data.VideoURL, outputPath)
}

// fallbackTikTokRegexExtract extracts video URL using regular expressions
func fallbackTikTokRegexExtract(ctx context.Context, htmlContent string, outputPath string) (string, error) {
	// Search for various video URL patterns
	patterns := []string{
		`"play":"([^"]+)"`,
		`"video_url":"([^"]+)"`,
		`"download_url":"([^"]+)"`,
		`href="([^"]*\.mp4[^"]*)"`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(htmlContent)
		if len(matches) > 1 {
			videoURL := matches[1]
			// Decode URL if needed
			videoURL = strings.ReplaceAll(videoURL, "\\u0026", "&")
			videoURL = strings.ReplaceAll(videoURL, "\\/", "/")

			return downloadTikTokURL(ctx, videoURL, outputPath)
		}
	}

	return "", fmt.Errorf("failed to find video URL in fallback mode for TikTok")
}

// fallbackFacebookDownload extracts public videos directly when SnapSave fails.
func fallbackFacebookDownload(ctx context.Context, url string, userID int64) (string, error) {
	outputPath, err := createUserDirectory(userID, "facebook")
	if err != nil {
		return "", err
	}
	return downloadFacebookYtDlp(ctx, url, outputPath, runYtDlp)
}

// downloadMedia downloads media by URL and saves it to outputPath
func downloadMedia(ctx context.Context, url, outputPath string) (string, error) {
	// Remove excess quotes and escaped characters in URL
	url = strings.Trim(url, "\"'")
	url = strings.ReplaceAll(url, "\\", "")

	// Check and fix relative URLs
	if strings.HasPrefix(url, "/") {
		url = "https://ddinstagram.com" + url
	}

	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("invalid URL format: %s", url)
	}

	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			lastErr = fmt.Errorf("error creating download request: %v", err)
			continue
		}

		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
		req.Header.Set("Accept", "video/mp4,video/webm,video/*;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Referer", "https://www.instagram.com/")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("error downloading video (attempt %d): %v", attempt+1, err)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("received invalid status code during download (attempt %d): %d", attempt+1, resp.StatusCode)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}

		contentType := resp.Header.Get("Content-Type")
		if !strings.Contains(contentType, "video/") && !strings.Contains(contentType, "application/octet-stream") && !strings.Contains(contentType, "binary/") {
			contentLength := resp.ContentLength
			if contentLength > 0 && contentLength < 10000 {
				lastErr = fmt.Errorf("content does not look like a video: type %s, size %d bytes", contentType, contentLength)
				continue
			}
		}

		out, err := os.Create(outputPath)
		if err != nil {
			return "", fmt.Errorf("error creating file: %v", err)
		}

		n, err := io.Copy(out, resp.Body)
		out.Close()

		if err != nil {
			os.Remove(outputPath)
			lastErr = fmt.Errorf("error writing video to file: %v", err)
			continue
		}

		if n < 1024 {
			os.Remove(outputPath)
			lastErr = fmt.Errorf("downloaded file is too small (%d bytes), possibly not a video", n)
			continue
		}

		return outputPath, nil
	}

	// If we are here, all attempts failed
	return "", fmt.Errorf("failed to download video after %d attempts: %v", maxRetries, lastErr)
}
