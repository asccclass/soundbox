package main

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ─── AudioPlayer 介面：直接播放音訊檔案（不需 TTS）────────────────────────────

// AudioPlayer 擴充 Speaker，增加直接播放音訊檔案的能力
type AudioPlayer interface {
	Speaker
	PlayFile(filePath string) error
}

// ─── 音訊檔案資訊 ──────────────────────────────────────────────────────────────

type AudioFileInfo struct {
	Path        string
	Ext         string        // mp3, wav, flac, aac, ogg, aiff
	ContentType string        // audio/mpeg, audio/wav ...
	Size        int64
	Duration    time.Duration // 需要 ffprobe
}

// probeAudioFile 取得音訊檔案基本資訊
func probeAudioFile(path string) (*AudioFileInfo, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("找不到檔案: %w", err)
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	ct := audioContentType(ext)
	if ct == "" {
		return nil, fmt.Errorf("不支援的音訊格式: .%s（支援 mp3/wav/flac/aac/ogg/aiff/m4a）", ext)
	}

	info := &AudioFileInfo{
		Path:        path,
		Ext:         ext,
		ContentType: ct,
		Size:        stat.Size(),
	}

	// 嘗試用 ffprobe 取得時長
	if commandExists("ffprobe") {
		out, err := exec.Command("ffprobe",
			"-v", "error",
			"-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1",
			path,
		).Output()
		if err == nil {
			var secs float64
			fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &secs)
			info.Duration = time.Duration(secs * float64(time.Second))
		}
	}

	return info, nil
}

func audioContentType(ext string) string {
	types := map[string]string{
		"mp3":  "audio/mpeg",
		"wav":  "audio/wav",
		"flac": "audio/flac",
		"aac":  "audio/aac",
		"ogg":  "audio/ogg",
		"aiff": "audio/aiff",
		"aif":  "audio/aiff",
		"m4a":  "audio/mp4",
		"opus": "audio/opus",
	}
	if ct, ok := types[ext]; ok {
		return ct
	}
	// 也嘗試 mime 套件
	return mime.TypeByExtension("." + ext)
}

// ─── Google Home：播放本地音訊檔案 ────────────────────────────────────────────

// PlayFile 讓 GoogleHomeSpeaker 直接播放音訊檔案
func (g *GoogleHomeSpeaker) PlayFile(filePath string) error {
	info, err := probeAudioFile(filePath)
	if err != nil {
		return err
	}

	printFileInfo("Google Home", info)

	audioURL, ct, stop, err := g.serveAudioFile(info.Path, info.Ext)
	if err != nil {
		return fmt.Errorf("啟動串流伺服器失敗: %w", err)
	}

	// 音訊時長已知就用真實時長，否則用大小估算
	ttl := info.Duration + 3*time.Second
	if ttl < 10*time.Second {
		ttl = estimateTTL(int(info.Size), info.Ext)
	}

	// castToDevice 負責在播放完成後呼叫 stop()
	return g.castToDevice(audioURL, ct, ttl, stop)
}

// ─── Apple HomePod：播放本地音訊檔案 ──────────────────────────────────────────

// PlayFile 讓 AppleHomeSpeaker 直接播放音訊檔案
func (a *AppleHomeSpeaker) PlayFile(filePath string) error {
	info, err := probeAudioFile(filePath)
	if err != nil {
		return err
	}

	printFileInfo("Apple HomePod", info)

	// 若格式不相容，先轉 WAV
	playFile := info.Path
	if info.Ext == "flac" || info.Ext == "ogg" || info.Ext == "opus" {
		converted, err := convertToWAV(info.Path, info.Ext)
		if err != nil {
			fmt.Printf("⚠️  格式轉換失敗，嘗試直接播放: %v\n", err)
		} else {
			defer os.Remove(converted)
			playFile = converted
		}
	}

	return a.playViaAirPlay(playFile)
}

// ─── 小愛同學：播放本地音訊檔案 ───────────────────────────────────────────────

// PlayFile 讓 XiaoAISpeaker 直接播放音訊檔案
func (x *XiaoAISpeaker) PlayFile(filePath string) error {
	info, err := probeAudioFile(filePath)
	if err != nil {
		return err
	}

	printFileInfo("小愛同學", info)

	// 方法 1: MiIO play_url 指令（需要本機 HTTP 伺服器）
	localIP, _ := getLocalIP()
	if localIP != "" && x.getDeviceIP() != "" {
		audioURL, stop, err := serveFileTemporarily(info.Path, info.ContentType, 19876)
		if err == nil {
			waitDur := info.Duration + 3*time.Second
			if waitDur < 10*time.Second {
				waitDur = 30 * time.Second
			}
			defer func() { time.Sleep(waitDur); stop() }()

			if err := x.playURLViaMiIO(audioURL); err == nil {
				return nil
			} else {
				fmt.Printf("⚠️  MiIO play_url 失敗，嘗試其他方式: %v\n", err)
			}
		}
	}

	// 方法 2: miiocli（python-miio）
	if commandExists("miiocli") {
		return x.playFileViaMiIOCli(info.Path)
	}

	// 方法 3: 雲端 play_url（若有雲端 token）
	if x.cfg.XiaoAIMiID != "" {
		audioURL, stop, err := serveFileTemporarily(info.Path, info.ContentType, 19876)
		if err != nil {
			return err
		}
		waitDur := info.Duration + 5*time.Second
		if waitDur < 15*time.Second {
			waitDur = 30 * time.Second
		}
		defer func() { time.Sleep(waitDur); stop() }()
		return x.playURLViaCloud(audioURL)
	}

	return fmt.Errorf("播放失敗：需要設定 XIAOAI_DEVICE_IP + XIAOAI_DEVICE_TOKEN 或 XIAOAI_MI_ID")
}

// playURLViaMiIO 發 MiIO play_url 指令
func (x *XiaoAISpeaker) playURLViaMiIO(url string) error {
	payload := map[string]interface{}{
		"id":     2,
		"method": "play_url",
		"params": []string{url},
	}
	return x.sendMiIOPacket(x.getDeviceIP(), os.Getenv("XIAOAI_DEVICE_TOKEN"), payload)
}

// playFileViaMiIOCli 透過 python-miio 播放
func (x *XiaoAISpeaker) playFileViaMiIOCli(path string) error {
	deviceIP := x.getDeviceIP()
	deviceToken := os.Getenv("XIAOAI_DEVICE_TOKEN")
	if deviceIP == "" || deviceToken == "" {
		return fmt.Errorf("需要 XIAOAI_DEVICE_IP 和 XIAOAI_DEVICE_TOKEN")
	}
	cmd := exec.Command("miiocli", "xioamiio",
		"--ip", deviceIP, "--token", deviceToken,
		"play", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("miiocli play 失敗: %w\n%s", err, out)
	}
	fmt.Printf("✅ [小愛同學] 播放成功: %s\n", out)
	return nil
}

// playURLViaCloud 透過小米雲端播放 URL
func (x *XiaoAISpeaker) playURLViaCloud(url string) error {
	// 重用現有雲端登入後，改用 play_url 指令
	return x.speakViaCloud("播放音訊：" + url) // fallback: 用 TTS 說出 URL（實際應改為 play_url API）
}

// ─── 通用：臨時 HTTP 檔案伺服器 ────────────────────────────────────────────────

// serveFileTemporarily 在指定 port 提供單一音訊檔案，回傳 URL 和停止函式
func serveFileTemporarily(filePath, contentType string, port int) (string, func(), error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/audio", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeFile(w, r, filePath)
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
	}

	go func() { srv.ListenAndServe() }()
	time.Sleep(150 * time.Millisecond) // 等伺服器就緒

	localIP, _ := getLocalIP()
	audioURL := fmt.Sprintf("http://%s:%d/audio", localIP, port)
	stop := func() { srv.Close() }
	return audioURL, stop, nil
}

// ─── 工廠：NewAudioPlayer ──────────────────────────────────────────────────────

// NewAudioPlayer 建立支援 PlayFile 的播放器
func NewAudioPlayer(cfg Config) (AudioPlayer, error) {
	switch cfg.SpeakerType {
	case GoogleHome:
		return NewGoogleHomeSpeaker(cfg)
	case AppleHome:
		return NewAppleHomeSpeaker(cfg)
	case XiaoAI:
		return NewXiaoAISpeaker(cfg)
	default:
		return nil, fmt.Errorf("不支援的音箱類型: %s", cfg.SpeakerType)
	}
}

// ─── 工具 ─────────────────────────────────────────────────────────────────────

func printFileInfo(speakerName string, info *AudioFileInfo) {
	dur := "未知"
	if info.Duration > 0 {
		mins := int(info.Duration.Minutes())
		secs := int(info.Duration.Seconds()) % 60
		dur = fmt.Sprintf("%d:%02d", mins, secs)
	}
	sizeMB := float64(info.Size) / 1024 / 1024
	fmt.Printf("🎵 [%s] 播放: %s\n", speakerName, filepath.Base(info.Path))
	fmt.Printf("   格式: .%s | 大小: %.2f MB | 時長: %s\n", info.Ext, sizeMB, dur)
}

// resolveAudioFiles 展開路徑（支援 glob、目錄）
func resolveAudioFiles(pattern string) ([]string, error) {
	// 1. 嘗試 glob
	matches, err := filepath.Glob(pattern)
	if err == nil && len(matches) > 0 {
		return filterAudioFiles(matches), nil
	}

	// 2. 若是目錄，列出其中音訊檔
	stat, err := os.Stat(pattern)
	if err != nil {
		return nil, fmt.Errorf("找不到路徑: %s", pattern)
	}
	if stat.IsDir() {
		var files []string
		entries, _ := os.ReadDir(pattern)
		for _, e := range entries {
			if !e.IsDir() {
				ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
				if audioContentType(ext) != "" {
					files = append(files, filepath.Join(pattern, e.Name()))
				}
			}
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("目錄中沒有音訊檔案: %s", pattern)
		}
		return files, nil
	}

	return []string{pattern}, nil
}

func filterAudioFiles(paths []string) []string {
	var result []string
	for _, p := range paths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if audioContentType(ext) != "" {
			result = append(result, p)
		}
	}
	return result
}
