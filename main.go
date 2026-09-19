package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"goland/VideoSaverBot/downloader"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var (
	instagramRegex = regexp.MustCompile(`^https?://(?:www\.)?instagram\.com/(?:p|reel|reels|tv|stories|share)/([^/?#&]+).*`)
	twitterRegex   = regexp.MustCompile(`^https://(?:x|twitter)\.com(?:/(?:i/web|[^/]+)/status/(\d+)(?:.*)?)?$`)
	tiktokRegex    = regexp.MustCompile(`^https?://(?:www\.|m\.|vm\.|vt\.)?tiktok\.com/(?:@[^/\s]+/(?:video|photo)/\d+|v/\d+|t/[\w]+|[\w]+)/?(?:[?#][^\s]*)?$`)
	facebookRegex  = regexp.MustCompile(`^https?://(?:www\.|web\.|m\.)?facebook\.com/(?:watch\?v=[0-9]+|watch/\?v=[0-9]+|reel/[0-9]+|[a-zA-Z0-9.\-_]+/(?:videos|posts)/[0-9]+|[0-9]+/(?:videos|posts)/[0-9]+|share/(?:v|r)/[a-zA-Z0-9]+)(?:[^/?#&]+.*)?$|^https://fb\.watch/[a-zA-Z0-9]+$`)
	youtubeRegex   = regexp.MustCompile(`^(?:https?://)?(?:(?:www|m)\.)?youtube\.com/shorts/([a-zA-Z0-9_-]{11})/?(?:[?#][^\s]*)?$`)

	_userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

	downloadSemaphore chan struct{}

	activeUsers  sync.Map
	queuedCount  int64
	runningCount int64
	statTotal    int64
	statErrors   int64
	statStart    = time.Now()
	adminID      int64
)

func main() {
	botTokenFlag := flag.String("token", "", "Telegram bot token")
	debugModeFlag := flag.Bool("debug", false, "Debug mode (true/false)")
	maxConcurrentDownloads := flag.Int("concurrent", 5, "Maximum number of concurrent downloads")
	flag.Parse()

	adminID, _ = strconv.ParseInt(os.Getenv("BOT_ADMIN_ID"), 10, 64)

	if err := checkYtDlpAvailability(); err != nil {
		log.Printf("Warning: YouTube and direct TikTok downloads require yt-dlp: %v", err)
	} else {
		log.Println("yt-dlp detected, YouTube and direct TikTok downloads enabled")
	}

	downloadSemaphore = make(chan struct{}, *maxConcurrentDownloads)

	botToken := *botTokenFlag
	if botToken == "" {
		botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
		if botToken == "" {
			log.Fatal("Bot token not found. Set TELEGRAM_BOT_TOKEN environment variable or use the -token flag")
		}
	}

	client, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Failed to initialize bot: %v", err)
	}

	client.Debug = *debugModeFlag
	log.Printf("Authorized on account %s (Debug mode: %v)", client.Self.UserName, client.Debug)

	setupBotCommands(client)

	go startPeriodicCleanup()

	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 30

	updates := client.GetUpdatesChan(updateConfig)

	connectionErrors := make(chan error)
	reconnect := make(chan struct{})

	// Start monitoring connection to Telegram API
	go monitorConnection(client, connectionErrors, reconnect)

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	wg := &sync.WaitGroup{}

	for {
		select {
		case update := <-updates:
			if update.Message != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					handleMessage(client, update.Message)
				}()
			}
		case <-shutdownCtx.Done():
			log.Println("Termination signal received, waiting for active downloads...")
			waitCh := make(chan struct{})
			go func() { wg.Wait(); close(waitCh) }()
			select {
			case <-waitCh:
				log.Println("All downloads completed")
			case <-time.After(30 * time.Second):
				log.Println("30s timeout reached, forcing shutdown")
			}
			return
		case err := <-connectionErrors:
			if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "EOF") {
				log.Printf("Telegram API connection error: %v", err)
			}

			reconnect <- struct{}{}
		case <-reconnect:
			updates = client.GetUpdatesChan(updateConfig)
		}
	}
}

// monitorConnection monitors the connection to Telegram API
func monitorConnection(bot *tgbotapi.BotAPI, errorChan chan<- error, reconnect chan<- struct{}) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		_, err := bot.GetMe()
		if err != nil {
			errorChan <- err
		}
	}
}

func setupBotCommands(bot *tgbotapi.BotAPI) {
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Start the bot"},
		{Command: "help", Description: "Show usage instructions"},
		{Command: "stats", Description: "Bot statistics (admin only)"},
	}

	_, err := bot.Request(tgbotapi.NewSetMyCommands(commands...))
	if err != nil {
		log.Printf("Failed to set bot commands: %v", err)
	}

	scope := tgbotapi.NewBotCommandScopeAllGroupChats()
	_, err = bot.Request(tgbotapi.NewSetMyCommandsWithScope(scope, commands...))
	if err != nil {
		log.Printf("Failed to set bot commands for group chats: %v", err)
	}
}

func isJustLink(text string, regex *regexp.Regexp) bool {
	trimmedText := strings.TrimSpace(text)

	matches := regex.FindAllString(trimmedText, -1)
	if len(matches) == 0 {
		return false
	}

	return len(trimmedText) == len(matches[0])
}

func extractLink(text string) string {
	text = strings.TrimSpace(text)
	instagramMatches := instagramRegex.FindStringSubmatch(text)
	if len(instagramMatches) > 0 {
		return instagramMatches[0]
	}

	twitterMatches := twitterRegex.FindStringSubmatch(text)
	if len(twitterMatches) > 0 {
		return twitterMatches[0]
	}

	tiktokMatches := tiktokRegex.FindStringSubmatch(text)
	if len(tiktokMatches) > 0 {
		return tiktokMatches[0]
	}

	facebookMatches := facebookRegex.FindStringSubmatch(text)
	if len(facebookMatches) > 0 {
		return facebookMatches[0]
	}

	youtubeMatches := youtubeRegex.FindStringSubmatch(text)
	if len(youtubeMatches) > 0 {
		link := youtubeMatches[0]
		if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			link = "https://" + link
		}
		return link
	}

	return text
}

func handleMessage(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	userID := message.From.ID
	chatID := message.Chat.ID
	isGroup := message.Chat.IsGroup() || message.Chat.IsSuperGroup()

	if bot.Debug {
		log.Printf("[%s] %s in chat %d (group: %v)", message.From.UserName, message.Text, chatID, isGroup)
	}

	if isGroup {
		mentionsBot := false
		if message.Entities != nil {
			for _, entity := range message.Entities {
				if entity.Type == "mention" {
					mention := message.Text[entity.Offset : entity.Offset+entity.Length]
					if strings.Contains(mention, "@"+bot.Self.UserName) {
						mentionsBot = true
						break
					}
				}
			}
		}

		if !message.IsCommand() && !mentionsBot &&
			!isJustLink(message.Text, instagramRegex) &&
			!isJustLink(message.Text, twitterRegex) &&
			!isJustLink(message.Text, tiktokRegex) &&
			!isJustLink(message.Text, facebookRegex) &&
			!isJustLink(message.Text, youtubeRegex) {
			return
		}
	}

	if message.IsCommand() {
		switch message.Command() {
		case "start":
			if isGroup {
				msg := tgbotapi.NewMessage(chatID,
					"Hi! I am ready to download videos from Instagram, Twitter, TikTok, Facebook, and YouTube Shorts. Just send me a link.")
				bot.Send(msg)
			} else {
				msg := tgbotapi.NewMessage(chatID,
					"Hi! I am a bot for downloading videos from Instagram, Twitter (X), TikTok, Facebook, and YouTube Shorts. "+
						"Just send me a link to the post, and I will save the video for you.\n\n")
				bot.Send(msg)
			}
			return
		case "help":
			helpText := "🔍 *How to use*:\n\n" +
				"1. Find a video on Instagram, Twitter (X), TikTok, Facebook, or YouTube Shorts\n" +
				"2. Copy the post/video link\n" +
				"3. Send me the link\n" +
				"4. Wait for the download and receive your video\n\n" +
				"*Supported platforms*:\n" +
				"• Instagram (posts and reels)\n" +
				"• Twitter/X\n" +
				"• TikTok\n" +
				"• Facebook\n" +
				"• YouTube Shorts (short videos only)\n\n" +
				"*YouTube*: Only Shorts are supported (youtube.com/shorts/). For regular videos, please use third-party websites.\n\n" +
				"*In group chats*: I only respond to video links or messages mentioning me (@" + bot.Self.UserName + ")"

			msg := tgbotapi.NewMessage(chatID, helpText)
			msg.ParseMode = "Markdown"
			bot.Send(msg)
			return
		case "stats":
			if adminID == 0 || userID != adminID {
				return
			}
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf(
				"Statistics:\nTotal downloads: %d\nErrors: %d\nIn queue: %d\nActive: %d\nUptime: %v",
				atomic.LoadInt64(&statTotal),
				atomic.LoadInt64(&statErrors),
				atomic.LoadInt64(&queuedCount),
				atomic.LoadInt64(&runningCount),
				time.Since(statStart).Round(time.Minute),
			)))
			return
		}
	}

	messageText := extractLink(message.Text)

	// Determine platform
	var processingText string
	switch {
	case instagramRegex.MatchString(messageText):
		processingText = "Processing Instagram link..."
	case twitterRegex.MatchString(messageText):
		processingText = "Processing Twitter/X link..."
	case tiktokRegex.MatchString(messageText):
		processingText = "Processing TikTok link..."
	case facebookRegex.MatchString(messageText):
		processingText = "Processing Facebook link..."
	case youtubeRegex.MatchString(messageText):
		processingText = "Processing YouTube link..."
	default:
		if !isGroup {
			normalYouTubeRegex := regexp.MustCompile(`^(?:https?://)?(?:www\.)?(?:youtube\.com/watch\?v=|youtu\.be/)([a-zA-Z0-9_-]{11})`)
			if normalYouTubeRegex.MatchString(messageText) {
				msg := tgbotapi.NewMessage(chatID,
					"I only support YouTube Shorts (short videos).\n\n"+
						"Links should be formatted as: youtube.com/shorts/VIDEO_ID\n\n"+
						"To download regular YouTube videos, use third-party websites such as:\n"+
						"• savefrom.net\n"+
						"• y2mate.com\n"+
						"• 9xbuddy.com")
				bot.Send(msg)
			} else {
				msg := tgbotapi.NewMessage(chatID,
					"Please send a link to a post from Instagram, Twitter, TikTok, Facebook, or YouTube Shorts containing a video.")
				bot.Send(msg)
			}
		}
		return
	}

	// Restriction: one request per user at a time
	if _, loaded := activeUsers.LoadOrStore(userID, struct{}{}); loaded {
		bot.Send(tgbotapi.NewMessage(chatID, "Your download is already being processed, please wait..."))
		return
	}
	defer activeUsers.Delete(userID)

	processingMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, processingText))

	// Semaphore with queue position feedback
	var queueMsg *tgbotapi.Message
	select {
	case downloadSemaphore <- struct{}{}:
		// slot available, queue not needed
	default:
		atomic.AddInt64(&queuedCount, 1)
		m, _ := bot.Send(tgbotapi.NewMessage(chatID,
			fmt.Sprintf("All download slots are busy, please wait... (in queue: %d)", atomic.LoadInt64(&queuedCount))))
		queueMsg = &m
		downloadSemaphore <- struct{}{} // blocking acquire
		atomic.AddInt64(&queuedCount, -1)
	}
	defer func() { <-downloadSemaphore }()

	if queueMsg != nil {
		bot.Request(tgbotapi.NewDeleteMessage(chatID, queueMsg.MessageID))
	}

	atomic.AddInt64(&runningCount, 1)
	defer atomic.AddInt64(&runningCount, -1)

	dlCtx, dlCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer dlCancel()

	var videoPath string
	var err error
	switch {
	case instagramRegex.MatchString(messageText):
		videoPath, err = downloader.DownloadInstagramVideo(dlCtx, messageText, userID)
	case twitterRegex.MatchString(messageText):
		videoPath, err = downloader.DownloadTwitterVideo(dlCtx, messageText, userID)
	case tiktokRegex.MatchString(messageText):
		videoPath, err = downloader.DownloadTikTokVideo(dlCtx, messageText, userID)
	case facebookRegex.MatchString(messageText):
		videoPath, err = downloader.DownloadFacebookVideo(dlCtx, messageText, userID)
	case youtubeRegex.MatchString(messageText):
		videoPath, err = downloader.DownloadYouTubeVideo(dlCtx, messageText, userID)
	}

	if err != nil {
		log.Printf("Download error for user %d: %v", userID, err)
		atomic.AddInt64(&statErrors, 1)
		errorMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Error downloading video: %v", err))
		bot.Send(errorMsg)
		go deleteMessageAfterDelay(bot, chatID, processingMsg.MessageID, 10)
		return
	}

	atomic.AddInt64(&statTotal, 1)
	sendVideo(bot, chatID, videoPath, userID, processingMsg.MessageID)
	go cleanupOldFiles(userID)
}

func deleteMessageAfterDelay(bot *tgbotapi.BotAPI, chatID int64, messageID int, delaySeconds int) {
	time.Sleep(time.Duration(delaySeconds) * time.Second)
	deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
	if _, err := bot.Request(deleteMsg); err != nil {
		log.Printf("Failed to delete message %d: %v", messageID, err)
	}
}

func getVideoDimensions(videoPath string) (width, height int) {
	type probeOutput struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	out, err := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_streams", videoPath).Output()
	if err != nil {
		return 0, 0
	}
	var data probeOutput
	if err := json.Unmarshal(out, &data); err != nil {
		return 0, 0
	}
	for _, s := range data.Streams {
		if s.CodecType == "video" {
			return s.Width, s.Height
		}
	}
	return 0, 0
}

func sendVideoWithDimensions(bot *tgbotapi.BotAPI, chatID int64, videoPath string, width, height int) error {
	f, err := os.Open(videoPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	_ = w.WriteField("width", strconv.Itoa(width))
	_ = w.WriteField("height", strconv.Itoa(height))
	_ = w.WriteField("supports_streaming", "true")
	part, err := w.CreateFormFile("video", filepath.Base(videoPath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	w.Close()

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendVideo", bot.Token)
	req, err := http.NewRequest("POST", apiURL, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := bot.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var apiResp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		return fmt.Errorf("telegram API: %s", apiResp.Description)
	}
	return nil
}

func sendVideo(bot *tgbotapi.BotAPI, chatID int64, videoPath string, userID int64, processingMsgID int) {
	var err error
	videoSent := false

	defer func() {
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, processingMsgID)
		if _, delErr := bot.Request(deleteMsg); delErr != nil {
			log.Printf("Failed to delete service message %d: %v", processingMsgID, delErr)
		}
		if videoSent {
			if fileErr := os.Remove(videoPath); fileErr != nil {
				log.Printf("Failed to delete temporary file %s: %v", videoPath, fileErr)
			}
		}
	}()

	width, height := getVideoDimensions(videoPath)
	if width > 0 && height > 0 {
		err = sendVideoWithDimensions(bot, chatID, videoPath, width, height)
	} else {
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(videoPath))
		video.SupportsStreaming = true
		_, err = bot.Send(video)
	}

	if err != nil {
		log.Printf("Error sending video to user %d: %v", userID, err)
		errorMsg := tgbotapi.NewMessage(chatID, "Failed to send video. Please try again.")
		bot.Send(errorMsg)
	} else {
		videoSent = true
	}
}

func cleanupOldFiles(userID int64) {
	userDir := filepath.Join("temp_videos", strconv.FormatInt(userID, 10))

	_, err := os.Stat(userDir)
	if os.IsNotExist(err) {
		return
	}

	files, err := os.ReadDir(userDir)
	if err != nil {
		log.Printf("Error reading user directory %d: %v", userID, err)
		return
	}

	now := time.Now()

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		filePath := filepath.Join(userDir, file.Name())
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			continue
		}

		if now.Sub(fileInfo.ModTime()) > time.Hour {
			if err := os.Remove(filePath); err != nil {
				log.Printf("Error deleting old file %s: %v", filePath, err)
			} else {
				log.Printf("Deleted old file: %s", filePath)
			}
		}
	}
}

func startPeriodicCleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	log.Println("Periodic cleanup of temporary files started")

	for range ticker.C {
		cleanupAllTempFiles()
	}
}

func cleanupAllTempFiles() {
	log.Println("Starting cleanup of all temporary files...")

	tempDir := "temp_videos"

	_, err := os.Stat(tempDir)
	if os.IsNotExist(err) {
		return
	}

	userDirs, err := os.ReadDir(tempDir)
	if err != nil {
		log.Printf("Error reading temporary files directory: %v", err)
		return
	}

	for _, userDir := range userDirs {
		if !userDir.IsDir() {
			continue
		}

		userDirPath := filepath.Join(tempDir, userDir.Name())

		files, err := os.ReadDir(userDirPath)
		if err != nil {
			log.Printf("Error reading user directory %s: %v", userDir.Name(), err)
			continue
		}

		now := time.Now()

		hasFiles := false

		for _, file := range files {
			if file.IsDir() {
				continue
			}

			filePath := filepath.Join(userDirPath, file.Name())
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				continue
			}

			if now.Sub(fileInfo.ModTime()) > 24*time.Hour {
				if err := os.Remove(filePath); err != nil {
					log.Printf("Error deleting old file %s: %v", filePath, err)
				} else {
					log.Printf("Deleted old file: %s", filePath)
				}
			} else {
				hasFiles = true
			}
		}

		if !hasFiles {
			if err := os.Remove(userDirPath); err != nil {
				log.Printf("Error deleting empty user directory %s: %v", userDirPath, err)
			} else {
				log.Printf("Deleted empty user directory: %s", userDirPath)
			}
		}
	}

	log.Println("Temporary file cleanup completed")
}

func checkYtDlpAvailability() error {
	return downloader.CheckYtDlpAvailability(context.Background())
}
