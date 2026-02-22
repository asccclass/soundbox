package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GoogleHomeSpeaker 透過 Chromecast 協定控制 Google Home
type GoogleHomeSpeaker struct {
	cfg        Config
	tts        TTSEngine
	serverPort int
	localIP    string
}

func NewGoogleHomeSpeaker(cfg Config) (*GoogleHomeSpeaker, error) {
	ttsEngine, err := NewTTSEngine(cfg.TTSEngine,
		os.Getenv("GOOGLE_TTS_API_KEY"),
		os.Getenv("AZURE_TTS_REGION"))
	if err != nil {
		return nil, err
	}
	localIP, _ := getLocalIP()
	if localIP == "" {
		localIP = "127.0.0.1"
	}
	return &GoogleHomeSpeaker{
		cfg:        cfg,
		tts:        ttsEngine,
		serverPort: 18765,
		localIP:    localIP,
	}, nil
}

func (g *GoogleHomeSpeaker) Name() string { return "Google Home" }

func (g *GoogleHomeSpeaker) Speak(text string) error {
	fmt.Printf("🔊 [Google Home] TTS 轉換中: %q\n", truncate(text, 50))

	audioData, ext, err := g.tts.TextToSpeech(text, g.cfg.Language, g.cfg.VoiceSpeed)
	if err != nil {
		return fmt.Errorf("TTS 失敗: %w", err)
	}

	audioFile, err := saveTempAudio(audioData, ext)
	if err != nil {
		return fmt.Errorf("儲存音訊失敗: %w", err)
	}
	defer os.Remove(audioFile)

	audioURL, ct, stop, err := g.serveAudioFile(audioFile, ext)
	if err != nil {
		return fmt.Errorf("啟動 HTTP 伺服器失敗: %w", err)
	}
	// 不在這裡 defer stop()，交給 castToDevice 控制生命週期
	_ = stop

	// TTS 音訊通常很短，預估時長（實際以 keepAlive 等待）
	estimatedTTL := estimateTTL(len(audioData), ext)

	return g.castToDevice(audioURL, ct, estimatedTTL, stop)
}

func (g *GoogleHomeSpeaker) SetVolume(level int) error {
	if !commandExists("catt") {
		return fmt.Errorf("需要安裝 catt 才能設定音量: pip install catt")
	}
	args := g.deviceArgs([]string{"volume", fmt.Sprintf("%d", level)})
	out, err := exec.Command("catt", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("設定音量失敗: %w\n%s", err, out)
	}
	return nil
}

func (g *GoogleHomeSpeaker) GetDevices() ([]Device, error) {
	if !commandExists("catt") {
		return nil, fmt.Errorf("需要安裝 catt: pip install catt")
	}
	out, err := exec.Command("catt", "scan").Output()
	if err != nil {
		return nil, fmt.Errorf("catt scan 失敗: %w", err)
	}
	var devices []Device
	for i, line := range splitLines(string(out)) {
		line = strings.TrimSpace(line)
		if line != "" && i > 0 {
			devices = append(devices, Device{
				ID: fmt.Sprintf("google-%d", i), Name: line, Type: "Google Home", IsOnline: true,
			})
		}
	}
	return devices, nil
}

// serveAudioFile 啟動臨時 HTTP 伺服器，回傳 (url, contentType, stopFn, err)
func (g *GoogleHomeSpeaker) serveAudioFile(filePath, ext string) (string, string, func(), error) {
	ctMap := map[string]string{
		"mp3": "audio/mpeg", "wav": "audio/wav", "flac": "audio/flac",
		"aac": "audio/aac", "ogg": "audio/ogg", "aiff": "audio/aiff", "m4a": "audio/mp4",
	}
	ct := ctMap[ext]
	if ct == "" {
		ct = "audio/mpeg"
	}

	mux := http.NewServeMux()
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeFile(w, r, filePath)
	}
	mux.HandleFunc("/audio", handler)
	mux.HandleFunc("/audio."+ext, handler)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", g.serverPort),
		Handler:      mux,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
	}
	go srv.ListenAndServe()
	time.Sleep(300 * time.Millisecond)

	url := fmt.Sprintf("http://%s:%d/audio.%s", g.localIP, g.serverPort, ext)
	fmt.Printf("   📡 串流位址: %s\n", url)
	return url, ct, func() { srv.Close() }, nil
}

// castToDevice 優先用純 Go Chromecast 協定，catt 為備用
func (g *GoogleHomeSpeaker) castToDevice(audioURL, contentType string, ttl time.Duration, stopServer func()) error {
	deviceIP := g.cfg.GoogleDeviceIP

	// ── 方法 1：純 Go Chromecast（保持連線直到播完）──────────────────────────
	if deviceIP != "" {
		fmt.Printf("   🎯 Chromecast 直連: %s\n", deviceIP)
		err := ChromecastPlay(deviceIP, audioURL, contentType, ttl)
		stopServer() // 播完後才關 HTTP Server
		if err != nil {
			fmt.Printf("   ⚠️  Chromecast 失敗，嘗試 catt: %v\n", err)
			// 繼續嘗試 catt（但 server 已關，catt 會失敗；如需支援 catt 備用需重啟 server）
		} else {
			return nil
		}
	}

	// ── 方法 2：catt（備用）──────────────────────────────────────────────────
	if commandExists("catt") {
		var args []string
		if deviceIP != "" {
			args = []string{"-d", deviceIP, "cast", audioURL}
		} else if g.cfg.GoogleDeviceName != "" {
			args = []string{"-d", g.cfg.GoogleDeviceName, "cast", audioURL}
		} else {
			args = []string{"cast", audioURL}
		}

		// catt 是同步的，等它結束後再關 server
		out, err := exec.Command("catt", args...).CombinedOutput()
		stopServer()
		if err != nil {
			outStr := string(out)
			if strings.Contains(outStr, "No devices found") {
				return fmt.Errorf("找不到裝置，請確認 --google-ip 正確")
			}
			return fmt.Errorf("catt 播放失敗: %w\n%s", err, out)
		}
		fmt.Println("✅ [Google Home] 播放成功（catt）")
		return nil
	}

	stopServer()
	if deviceIP == "" {
		return fmt.Errorf("請指定 --google-ip <Google Home 的 IP 位址>")
	}
	return fmt.Errorf("Chromecast 播放失敗，也找不到 catt（pip install catt）")
}

// deviceArgs 把 IP 或名稱前綴注入 catt 參數
func (g *GoogleHomeSpeaker) deviceArgs(args []string) []string {
	if g.cfg.GoogleDeviceIP != "" {
		return append([]string{"-d", g.cfg.GoogleDeviceIP}, args...)
	}
	if g.cfg.GoogleDeviceName != "" {
		return append([]string{"-d", g.cfg.GoogleDeviceName}, args...)
	}
	return args
}

// estimateTTL 根據檔案大小和格式估算播放時長（無 ffprobe 時的備用）
func estimateTTL(byteLen int, ext string) time.Duration {
	var bitsPerSecond int
	switch ext {
	case "mp3":
		bitsPerSecond = 128 * 1024
	case "wav":
		bitsPerSecond = 16 * 44100 * 2 // 16bit 44.1kHz stereo
	case "aiff":
		bitsPerSecond = 16 * 44100 * 2
	default:
		bitsPerSecond = 128 * 1024
	}
	secs := float64(byteLen*8) / float64(bitsPerSecond)
	ttl := time.Duration(secs)*time.Second + 3*time.Second // 加 3 秒緩衝
	if ttl < 10*time.Second {
		ttl = 10 * time.Second
	}
	return ttl
}

// ─── 工具 ─────────────────────────────────────────────────────────────────────

func getLocalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n]) + "..."
	}
	return s
}
