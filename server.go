package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─── 播放管理器：支援打斷播放 ─────────────────────────────────────────────────

type PlaybackManager struct {
	mu         sync.Mutex
	current    AudioPlayer
	stopChan   chan struct{}
	isPlaying  bool
	volume     int
	speaker    string
	finishChan chan struct{}
}

var playbackMgr = PlaybackManager{volume: 50}

func (pm *PlaybackManager) Play(p AudioPlayer) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.isPlaying && pm.current != nil {
		fmt.Println("🛑 正在打斷當前播放...")
		pm.stopChan <- struct{}{}
		time.Sleep(500 * time.Millisecond)
	}

	pm.current = p
	pm.stopChan = make(chan struct{})
	pm.finishChan = make(chan struct{})
	pm.isPlaying = true
}

func (pm *PlaybackManager) MarkFinished() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.finishChan != nil {
		close(pm.finishChan)
	}
}

func (pm *PlaybackManager) SetVolume(level int) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if !pm.isPlaying || pm.current == nil {
		return fmt.Errorf("目前沒有正在播放的內容")
	}

	pm.volume = level
	return pm.current.SetVolume(level)
}

func (pm *PlaybackManager) GetVolume() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.volume
}

func (pm *PlaybackManager) SetSpeaker(s string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.speaker = s
}

func (pm *PlaybackManager) GetSpeaker() string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.speaker
}

func (pm *PlaybackManager) Stop() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if !pm.isPlaying || pm.current == nil {
		return fmt.Errorf("目前沒有正在播放的內容")
	}

	pm.stopChan <- struct{}{}
	pm.isPlaying = false
	pm.current = nil
	return nil
}

func (pm *PlaybackManager) IsPlaying() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.isPlaying
}

func (pm *PlaybackManager) GetStopChan() <-chan struct{} {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.stopChan == nil {
		return nil
	}
	return pm.stopChan
}

func (pm *PlaybackManager) GetFinishChan() <-chan struct{} {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.finishChan == nil {
		return nil
	}
	return pm.finishChan
}

// ─── HTTP REST API 伺服器 ─────────────────────────────────────────────────────

type SpeakRequest struct {
	Text       string  `json:"text"`
	Speaker    string  `json:"speaker"`    // "google" | "apple" | "xiaoai"
	Language   string  `json:"language"`   // "zh-TW" | "zh-CN" | "en-US"
	TTSEngine  string  `json:"tts_engine"` // "local" | "google" | "azure"
	VoiceSpeed float64 `json:"speed"`
	Volume     int     `json:"volume"`
}

type APIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

func runHTTPServer(port int) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/speak", handleSpeak)
	mux.HandleFunc("/api/play", handlePlayFile)
	mux.HandleFunc("/api/play-upload", handlePlayUpload)
	mux.HandleFunc("/api/stop", handleStop)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/volume", handleVolume)
	mux.HandleFunc("/api/devices", handleListDevices)
	mux.HandleFunc("/health", handleHealth)

	// 靜態頁面：簡易 Web UI
	mux.HandleFunc("/", handleWebUI)

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("🚀 Voice Briefing API 伺服器啟動: http://localhost%s\n", addr)
	fmt.Println("📋 API 端點:")
	fmt.Printf("   POST http://localhost%s/api/speak\n", addr)
	fmt.Printf("   POST http://localhost%s/api/stop - 打斷播放\n", addr)
	fmt.Printf("   GET  http://localhost%s/api/status - 播放狀態\n", addr)
	fmt.Printf("   GET  http://localhost%s/api/devices\n", addr)
	fmt.Printf("   GET  http://localhost%s\n", addr)

	return http.ListenAndServe(addr, mux)
}

func handleSpeak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "只支援 POST 方法", http.StatusMethodNotAllowed)
		return
	}

	var req SpeakRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON 格式錯誤: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Text == "" {
		jsonError(w, "text 欄位不能為空", http.StatusBadRequest)
		return
	}
	if req.Speaker == "" {
		jsonError(w, "speaker 欄位不能為空（google|apple|xiaoai）", http.StatusBadRequest)
		return
	}
	if req.Language == "" {
		req.Language = "zh-TW"
	}
	if req.TTSEngine == "" {
		req.TTSEngine = "local"
	}
	if req.VoiceSpeed == 0 {
		req.VoiceSpeed = 1.0
	}
	if req.Volume == 0 {
		req.Volume = 70
	}

	cfg := Config{
		SpeakerType:      SpeakerType(req.Speaker),
		Language:         req.Language,
		TTSEngine:        req.TTSEngine,
		VoiceSpeed:       req.VoiceSpeed,
		Volume:           req.Volume,
		GoogleDeviceIP:   os.Getenv("GOOGLE_DEVICE_IP"),
		GoogleDeviceName: os.Getenv("GOOGLE_DEVICE_NAME"),
		AppleDeviceName:  os.Getenv("APPLE_DEVICE_NAME"),
		XiaoAIDeviceID:   os.Getenv("XIAOAI_DEVICE_ID"),
		XiaoAIMiID:       os.Getenv("XIAOAI_MI_ID"),
		XiaoAIPassword:   os.Getenv("XIAOAI_PASSWORD"),
	}

	speaker, err := NewSpeaker(cfg)
	if err != nil {
		jsonError(w, "建立音箱失敗: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := speaker.Speak(req.Text); err != nil {
		jsonError(w, "播放失敗: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jsonSuccess(w, fmt.Sprintf("已透過 %s 播放: %s", speaker.Name(), truncate(req.Text, 30)))
}

// handlePlayFile POST /api/play  body: {"file":"/path/to/file.mp3","speaker":"google","volume":75}
func handlePlayFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "只支援 POST 方法", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		File    string `json:"file"`
		Speaker string `json:"speaker"`
		Volume  int    `json:"volume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON 格式錯誤: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.File == "" {
		jsonError(w, "file 欄位不能為空", http.StatusBadRequest)
		return
	}
	if req.Speaker == "" {
		jsonError(w, "speaker 欄位不能為空（google|apple|xiaoai）", http.StatusBadRequest)
		return
	}
	if req.Volume == 0 {
		req.Volume = 70
	}

	cfg := Config{
		SpeakerType:      SpeakerType(req.Speaker),
		Language:         "zh-TW",
		TTSEngine:        "local",
		VoiceSpeed:       1.0,
		Volume:           req.Volume,
		GoogleDeviceIP:   os.Getenv("GOOGLE_DEVICE_IP"),
		GoogleDeviceName: os.Getenv("GOOGLE_DEVICE_NAME"),
		AppleDeviceName:  os.Getenv("APPLE_DEVICE_NAME"),
		XiaoAIDeviceID:   os.Getenv("XIAOAI_DEVICE_ID"),
		XiaoAIMiID:       os.Getenv("XIAOAI_MI_ID"),
		XiaoAIPassword:   os.Getenv("XIAOAI_PASSWORD"),
	}

	player, err := NewAudioPlayer(cfg)
	if err != nil {
		jsonError(w, "建立播放器失敗: "+err.Error(), http.StatusInternalServerError)
		return
	}

	playbackMgr.Play(player)
	playbackMgr.SetSpeaker(req.Speaker)
	playbackMgr.mu.Lock()
	playbackMgr.volume = req.Volume
	playbackMgr.mu.Unlock()
	stopChan := playbackMgr.GetStopChan()

	// 等待播放完成或被中斷
	go func() {
		select {
		case <-stopChan:
			fmt.Println("🛑 播放已被打斷")
			playbackMgr.mu.Lock()
			playbackMgr.isPlaying = false
			playbackMgr.mu.Unlock()
			return
		default:
		}
		if err := player.PlayFile(req.File); err != nil {
			fmt.Printf("❌ [API] 播放失敗: %v\n", err)
			playbackMgr.mu.Lock()
			playbackMgr.isPlaying = false
			playbackMgr.mu.Unlock()
			return
		}
		// 播放成功完成
		fmt.Println("✅ 播放完畢")
		playbackMgr.MarkFinished()
		playbackMgr.mu.Lock()
		playbackMgr.isPlaying = false
		playbackMgr.mu.Unlock()
	}()

	jsonSuccess(w, fmt.Sprintf("已開始透過 %s 播放: %s", player.Name(), req.File))
}

// handlePlayUpload POST /api/play-upload  multipart/form-data: file, speaker, volume
func handlePlayUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "只支援 POST 方法", http.StatusMethodNotAllowed)
		return
	}

	// 最大接收 100 MB
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		jsonError(w, "解析表單失敗: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "取得上傳檔案失敗: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	speaker := r.FormValue("speaker")
	if speaker == "" {
		jsonError(w, "speaker 欄位不能為空", http.StatusBadRequest)
		return
	}
	volume := 70
	fmt.Sscanf(r.FormValue("volume"), "%d", &volume)

	// 儲存上傳的檔案到暫存目錄
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
	tmp, err := os.CreateTemp("", fmt.Sprintf("vb_upload_*.%s", ext))
	if err != nil {
		jsonError(w, "建立暫存檔案失敗: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			tmp.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	tmp.Close()

	cfg := Config{
		SpeakerType:      SpeakerType(speaker),
		Language:         "zh-TW",
		TTSEngine:        "local",
		VoiceSpeed:       1.0,
		Volume:           volume,
		GoogleDeviceIP:   os.Getenv("GOOGLE_DEVICE_IP"),
		GoogleDeviceName: os.Getenv("GOOGLE_DEVICE_NAME"),
		AppleDeviceName:  os.Getenv("APPLE_DEVICE_NAME"),
		XiaoAIDeviceID:   os.Getenv("XIAOAI_DEVICE_ID"),
		XiaoAIMiID:       os.Getenv("XIAOAI_MI_ID"),
		XiaoAIPassword:   os.Getenv("XIAOAI_PASSWORD"),
	}

	player, err := NewAudioPlayer(cfg)
	if err != nil {
		os.Remove(tmpPath)
		jsonError(w, "建立播放器失敗: "+err.Error(), http.StatusInternalServerError)
		return
	}

	playbackMgr.Play(player)
	playbackMgr.SetSpeaker(speaker)
	playbackMgr.mu.Lock()
	playbackMgr.volume = volume
	playbackMgr.mu.Unlock()
	stopChan := playbackMgr.GetStopChan()

	// 等待播放完成或被中斷
	go func() {
		defer os.Remove(tmpPath)
		select {
		case <-stopChan:
			fmt.Println("🛑 播放已被打斷")
			playbackMgr.mu.Lock()
			playbackMgr.isPlaying = false
			playbackMgr.mu.Unlock()
			return
		default:
		}
		if err := player.PlayFile(tmpPath); err != nil {
			fmt.Printf("❌ [Upload] 播放失敗: %v\n", err)
			playbackMgr.mu.Lock()
			playbackMgr.isPlaying = false
			playbackMgr.mu.Unlock()
			return
		}
		// 播放成功完成
		fmt.Println("✅ 播放完畢")
		playbackMgr.MarkFinished()
		playbackMgr.mu.Lock()
		playbackMgr.isPlaying = false
		playbackMgr.mu.Unlock()
	}()

	jsonSuccess(w, fmt.Sprintf("已開始透過 %s 播放: %s (%.1f MB)",
		player.Name(), header.Filename, float64(header.Size)/1024/1024))
}

func handleListDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "只支援 GET 方法", http.StatusMethodNotAllowed)
		return
	}

	result := map[string][]Device{}
	speakerTypes := []SpeakerType{GoogleHome, AppleHome, XiaoAI}
	cfg := Config{TTSEngine: "local", Language: "zh-TW"}

	for _, st := range speakerTypes {
		cfg.SpeakerType = st
		speaker, err := NewSpeaker(cfg)
		if err != nil {
			continue
		}
		devices, _ := speaker.GetDevices()
		result[string(st)] = devices
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		jsonError(w, "只支援 POST 或 GET 方法", http.StatusMethodNotAllowed)
		return
	}

	if err := playbackMgr.Stop(); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonSuccess(w, "已停止當前播放")
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "只支援 GET 方法", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"isPlaying": playbackMgr.IsPlaying(),
		"volume":    playbackMgr.GetVolume(),
		"speaker":   playbackMgr.GetSpeaker(),
	})
}

func handleVolume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "只支援 POST 方法", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Volume int `json:"volume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "JSON 格式錯誤: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Volume < 0 {
		req.Volume = 0
	}
	if req.Volume > 100 {
		req.Volume = 100
	}

	if err := playbackMgr.SetVolume(req.Volume); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonSuccess(w, fmt.Sprintf("音量已調整為 %d%%", req.Volume))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleWebUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, webUIHTML)
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(APIResponse{Success: false, Error: msg})
}

func jsonSuccess(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(APIResponse{Success: true, Message: msg})
}

// ─── 簡易 Web UI ──────────────────────────────────────────────────────────────

const webUIHTML = `<!DOCTYPE html>
<html lang="zh-TW">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>🔊 Voice Briefing</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; background: #0f0f1a; color: #e0e0e0; min-height: 100vh; display: flex; align-items: center; justify-content: center; }
    .card { background: #1a1a2e; border-radius: 20px; padding: 2rem; max-width: 500px; width: 92%; box-shadow: 0 24px 80px rgba(0,0,0,0.6); }
    h1 { font-size: 1.7rem; margin-bottom: 1.5rem; text-align: center; letter-spacing: 1px; }

    /* 分頁 */
    .tabs { display: flex; gap: 0; margin-bottom: 1.5rem; border-radius: 10px; overflow: hidden; border: 1px solid #333; }
    .tab { flex: 1; padding: 0.65rem; background: transparent; color: #888; border: none; cursor: pointer; font-size: 0.9rem; transition: all 0.2s; }
    .tab.active { background: #e94560; color: #fff; font-weight: 600; }

    /* 音箱選擇 */
    .speaker-btns { display: flex; gap: 0.5rem; margin-bottom: 1.2rem; }
    .speaker-btn { flex: 1; padding: 0.75rem 0.5rem; border: 2px solid #333; background: transparent; color: #ccc; border-radius: 10px; cursor: pointer; font-size: 0.85rem; transition: all 0.2s; }
    .speaker-btn.active { border-color: #e94560; background: rgba(233,69,96,0.15); color: #fff; }

    textarea { width: 100%; padding: 1rem; background: #0d1b2a; border: 1px solid #333; border-radius: 10px; color: #eee; font-size: 1rem; resize: vertical; min-height: 110px; margin-bottom: 1rem; outline: none; }
    textarea:focus { border-color: #e94560; }

    /* 檔案播放區 */
    .file-zone { border: 2px dashed #444; border-radius: 12px; padding: 1.5rem; text-align: center; margin-bottom: 1rem; cursor: pointer; transition: all 0.2s; background: #0d1b2a; }
    .file-zone:hover, .file-zone.drag-over { border-color: #e94560; background: rgba(233,69,96,0.05); }
    .file-zone input { display: none; }
    .file-zone .icon { font-size: 2.5rem; margin-bottom: 0.5rem; }
    .file-zone .hint { color: #888; font-size: 0.85rem; margin-top: 0.3rem; }
    .file-selected { color: #4caf50; font-weight: 600; margin-top: 0.5rem; font-size: 0.9rem; word-break: break-all; }

    .path-input { width: 100%; padding: 0.75rem 1rem; background: #0d1b2a; border: 1px solid #333; border-radius: 10px; color: #eee; font-size: 0.9rem; margin-bottom: 1rem; outline: none; }
    .path-input:focus { border-color: #e94560; }
    .path-hint { font-size: 0.78rem; color: #666; margin-bottom: 1rem; }

    /* 設定列 */
    .settings { display: grid; grid-template-columns: 1fr 1fr; gap: 0.8rem; margin-bottom: 1.2rem; }
    label { font-size: 0.78rem; color: #888; display: block; margin-bottom: 0.3rem; }
    select { width: 100%; background: #0d1b2a; border: 1px solid #333; border-radius: 8px; color: #eee; padding: 0.5rem; font-size: 0.85rem; }
    input[type=range] { width: 100%; accent-color: #e94560; }

    .action-btn { width: 100%; padding: 0.95rem; background: #e94560; border: none; border-radius: 12px; color: white; font-size: 1rem; cursor: pointer; transition: background 0.2s; font-weight: 600; letter-spacing: 0.5px; }
    .action-btn:hover { background: #c73652; }
    .action-btn:disabled { background: #444; cursor: not-allowed; }

    .stop-btn { width: 100%; padding: 0.75rem; background: #444; border: none; border-radius: 10px; color: #aaa; font-size: 0.9rem; cursor: pointer; margin-bottom: 1rem; transition: all 0.2s; }
    .stop-btn:hover { background: #f44336; color: white; }
    .stop-btn.playing { background: #f44336; color: white; animation: pulse 1.5s infinite; }
    @keyframes pulse { 0% { opacity: 1; } 50% { opacity: 0.7; } 100% { opacity: 1; } }

    .playing-indicator { display: none; text-align: center; padding: 0.5rem; margin-bottom: 1rem; background: rgba(76,175,80,0.15); border: 1px solid #4caf5055; border-radius: 8px; color: #81c784; font-size: 0.85rem; }
    .playing-indicator.active { display: block; }

    .volume-control { display: none; padding: 1rem; background: #0d1b2a; border-radius: 10px; margin-bottom: 1rem; }
    .volume-control.active { display: block; }
    .volume-control label { display: block; font-size: 0.85rem; color: #aaa; margin-bottom: 0.5rem; }
    .volume-control input[type=range] { width: 100%; }

    .status { margin-top: 1rem; padding: 0.8rem 1rem; border-radius: 8px; text-align: center; display: none; font-size: 0.9rem; }
    .status.success { background: rgba(76,175,80,0.15); color: #81c784; border: 1px solid #4caf5055; display: block; }
    .status.error { background: rgba(244,67,54,0.15); color: #ef9a9a; border: 1px solid #f4433655; display: block; }

    .pane { display: none; }
    .pane.active { display: block; }
  </style>
</head>
<body>
<div class="card">
  <h1>🔊 Voice Briefing</h1>

  <!-- 音箱選擇（共用） -->
  <div class="speaker-btns">
    <button class="speaker-btn active" data-speaker="google" onclick="selectSpeaker(this)">🏠 Google Home</button>
    <button class="speaker-btn" data-speaker="apple" onclick="selectSpeaker(this)">🎵 HomePod</button>
    <button class="speaker-btn" data-speaker="xiaoai" onclick="selectSpeaker(this)">🤖 小愛同學</button>
  </div>

  <!-- 播放狀態與停止按鈕 -->
  <div class="playing-indicator" id="playingIndicator">🔊 正在播放...</div>
  <button class="stop-btn" id="stopBtn" onclick="stopPlayback()">⏹️ 停止播放</button>

  <!-- 即時音量調整 -->
  <div class="volume-control" id="volumeControl" style="display:none;">
    <label>音量: <span id="volValLive">70</span>%</label>
    <input type="range" id="volumeLive" min="0" max="100" step="5" value="70"
      oninput="document.getElementById('volValLive').textContent=this.value"
      onchange="adjustVolume(this.value)">
  </div>

  <!-- 分頁切換 -->
  <div class="tabs">
    <button class="tab active" onclick="switchTab('tts', this)">🗣️ 文字轉語音</button>
    <button class="tab" onclick="switchTab('file', this)">🎵 播放音訊檔案</button>
  </div>

  <!-- TTS 分頁 -->
  <div class="pane active" id="pane-tts">
    <textarea id="text" placeholder="輸入要播放的文字...&#10;&#10;例如：今日簡報：台灣股市上漲，天氣晴朗。"></textarea>
    <div class="settings">
      <div>
        <label>語言</label>
        <select id="lang">
          <option value="zh-TW">繁體中文</option>
          <option value="zh-CN">簡體中文</option>
          <option value="en-US">English</option>
        </select>
      </div>
      <div>
        <label>TTS 引擎</label>
        <select id="tts">
          <option value="local">本地 TTS（免費）</option>
          <option value="google">Google Cloud</option>
          <option value="azure">Azure Neural</option>
        </select>
      </div>
      <div>
        <label>語速: <span id="speedVal">1.0</span></label>
        <input type="range" id="speed" min="0.5" max="2.0" step="0.1" value="1.0"
          oninput="document.getElementById('speedVal').textContent=parseFloat(this.value).toFixed(1)">
      </div>
      <div>
        <label>音量: <span id="volValTTS">70</span>%</label>
        <input type="range" id="volumeTTS" min="0" max="100" step="5" value="70"
          oninput="document.getElementById('volValTTS').textContent=this.value">
      </div>
    </div>
    <button class="action-btn" onclick="speak()" id="speakBtn">🎙️ 播放語音</button>
  </div>

  <!-- 檔案播放分頁 -->
  <div class="pane" id="pane-file">
    <!-- 拖曳上傳區 -->
    <div class="file-zone" id="fileZone"
         onclick="document.getElementById('fileInput').click()"
         ondragover="handleDragOver(event)"
         ondragleave="handleDragLeave(event)"
         ondrop="handleDrop(event)">
      <input type="file" id="fileInput" accept=".mp3,.wav,.flac,.aac,.ogg,.aiff,.m4a,.opus"
             onchange="handleFileSelect(this)">
      <div class="icon">🎵</div>
      <div>點擊選擇或拖曳音訊檔案</div>
      <div class="hint">支援 MP3 · WAV · FLAC · AAC · OGG · AIFF · M4A</div>
      <div class="file-selected" id="selectedFileName"></div>
    </div>

    <!-- 或輸入伺服器本地路徑 -->
    <input class="path-input" id="filePath" type="text"
           placeholder="或輸入伺服器本地路徑：/home/user/music/news.mp3">
    <div class="path-hint">💡 當伺服器與音箱在同一網路時，直接輸入路徑更快（不需上傳）</div>

    <div class="settings" style="grid-template-columns: 1fr;">
      <div>
        <label>音量: <span id="volValFile">70</span>%</label>
        <input type="range" id="volumeFile" min="0" max="100" step="5" value="70"
          oninput="document.getElementById('volValFile').textContent=this.value">
      </div>
    </div>
    <button class="action-btn" onclick="playFile()" id="playBtn">▶️ 播放音訊</button>
  </div>

  <div class="status" id="status"></div>
</div>

<script>
let currentSpeaker = 'google';
let selectedFile = null;

function selectSpeaker(btn) {
  document.querySelectorAll('.speaker-btn').forEach(b => b.classList.remove('active'));
  btn.classList.add('active');
  currentSpeaker = btn.dataset.speaker;
}

function switchTab(name, btn) {
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  document.querySelectorAll('.pane').forEach(p => p.classList.remove('active'));
  btn.classList.add('active');
  document.getElementById('pane-' + name).classList.add('active');
  clearStatus();
}

// ── TTS ─────────────────────────────────────────────────────────────────────

async function speak() {
  const text = document.getElementById('text').value.trim();
  if (!text) { showStatus('請輸入要播放的文字', false); return; }

  const btn = document.getElementById('speakBtn');
  setLoading(btn, '⏳ 轉換中...', true);

  try {
    const resp = await fetch('/api/speak', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        text,
        speaker: currentSpeaker,
        language: document.getElementById('lang').value,
        tts_engine: document.getElementById('tts').value,
        speed: parseFloat(document.getElementById('speed').value),
        volume: parseInt(document.getElementById('volumeTTS').value),
      })
    });
      const data = await resp.json();
      data.success ? showStatus('✅ ' + data.message, true) : showStatus('❌ ' + (data.error || '播放失敗'), false);
      if (data.success) updatePlayingStatus(true);
    } catch (e) {
      showStatus('❌ 網路錯誤: ' + e.message, false);
    } finally {
      setLoading(btn, '▶️ 播放音訊', false);
    }
    return;
  }

  // 模式 B：上傳本地檔案
  if (!selectedFile) {
    showStatus('請選擇音訊檔案或輸入伺服器路徑', false);
    return;
  }

  setLoading(btn, '⏳ 上傳並播放...', true);
  try {
    const formData = new FormData();
    formData.append('file', selectedFile);
    formData.append('speaker', currentSpeaker);
    formData.append('volume', volume);

    const resp = await fetch('/api/play-upload', {
      method: 'POST',
      body: formData
    });
    const data = await resp.json();
    data.success ? showStatus('✅ ' + data.message, true) : showStatus('❌ ' + (data.error || '播放失敗'), false);
    if (data.success) updatePlayingStatus(true);
  } catch (e) {
    showStatus('❌ 網路錯誤: ' + e.message, false);
  } finally {
    setLoading(btn, '▶️ 播放音訊', false);
  }
}

// ── 工具 ─────────────────────────────────────────────────────────────────────

function setLoading(btn, text, disabled) { btn.textContent = text; btn.disabled = disabled; }
function clearStatus() { document.getElementById('status').className = 'status'; }
function showStatus(msg, ok) {
  const el = document.getElementById('status');
  el.className = 'status ' + (ok ? 'success' : 'error');
  el.textContent = msg;
}

function updatePlayingStatus(isPlaying, volume) {
  const indicator = document.getElementById('playingIndicator');
  const stopBtn = document.getElementById('stopBtn');
  const volControl = document.getElementById('volumeControl');
  const volSlider = document.getElementById('volumeLive');
  if (isPlaying) {
    indicator.classList.add('active');
    stopBtn.classList.add('playing');
    stopBtn.textContent = '⏹️ 打斷播放';
    volControl.classList.add('active');
    if (volume !== undefined) {
      volSlider.value = volume;
      document.getElementById('volValLive').textContent = volume;
    }
  } else {
    indicator.classList.remove('active');
    stopBtn.classList.remove('playing');
    stopBtn.textContent = '⏹️ 停止播放';
    volControl.classList.remove('active');
  }
}

async function checkPlaybackStatus() {
  try {
    const resp = await fetch('/api/status');
    const data = await resp.json();
    updatePlayingStatus(data.isPlaying, data.volume);
  } catch (e) {
    console.error('檢查播放狀態失敗:', e);
  }
}

async function adjustVolume(volume) {
  try {
    const resp = await fetch('/api/volume', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ volume: parseInt(volume) })
    });
    const data = await resp.json();
    if (data.success) {
      console.log('音量已調整:', volume);
    }
  } catch (e) {
    console.error('調整音量失敗:', e);
  }
}

async function stopPlayback() {
  const btn = document.getElementById('stopBtn');
  btn.textContent = '⏳ 停止中...';
  try {
    const resp = await fetch('/api/stop', { method: 'POST' });
    const data = await resp.json();
    if (data.success) {
      updatePlayingStatus(false);
      showStatus('✅ 已停止播放', true);
    } else {
      showStatus('❌ ' + (data.error || '停止失敗'), false);
    }
  } catch (e) {
    showStatus('❌ 網路錯誤: ' + e.message, false);
  }
  btn.textContent = '⏹️ 停止播放';
}

// 定期檢查播放狀態
setInterval(checkPlaybackStatus, 2000);
checkPlaybackStatus();

document.getElementById('text').addEventListener('keydown', e => {
  if (e.ctrlKey && e.key === 'Enter') speak();
});
</script>
</body>
</html>`
