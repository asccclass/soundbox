# 🔊 Voice Briefing

用 **Golang** 實作的語音簡報工具，支援將文字透過 TTS 或直接串流音訊檔案到三種主流智慧音箱。

| 音箱 | 協定 | 連線方式 |
|------|------|----------|
| 🏠 Google Home / Nest | Chromecast (TLS TCP:8009) | 純 Go 實作，不依賴外部工具 |
| 🎵 Apple HomePod | AirPlay / afplay | raop-client / macOS 系統路由 |
| 🤖 小愛同學 | MiIO (UDP:54321) + 小米雲端 | AES-128-CBC 加密指令 |

---

## 📁 檔案結構

```
voice_briefing/
├── main.go          # 程式進入點，註冊所有 CLI 指令
├── speaker.go       # Speaker / AudioPlayer 介面定義、Config 結構、工廠方法
├── tts.go           # TTS 引擎（Google Cloud TTS / Azure Neural TTS / 本地 say/espeak）
├── chromecast.go    # 純 Go Chromecast 協定實作（TLS + protobuf 手工編解碼）
├── google_home.go   # Google Home 控制器（HTTP 串流伺服器 + Chromecast LOAD）
├── apple_home.go    # Apple HomePod 控制器（AirPlay / afplay / AppleScript）
├── xiaoai.go        # 小愛同學控制器（MiIO 本地 + 小米雲端 API）
├── play_file.go     # 音訊檔案直接播放（AudioPlayer 介面、格式探測、路徑解析）
├── commands.go      # CLI 指令：speak / play / list / serve
├── server.go        # HTTP REST API 伺服器 + 嵌入式 Web UI
└── go.mod           # 模組定義（僅依賴 spf13/cobra）
```

---

## 🏗️ 架構設計

### 核心介面

```go
// Speaker — 所有音箱的通用介面
type Speaker interface {
    Name() string
    Speak(text string) error      // 文字 → TTS → 播放
    SetVolume(level int) error
    GetDevices() ([]Device, error)
}

// AudioPlayer — 擴充 Speaker，支援直接播放音訊檔案
type AudioPlayer interface {
    Speaker
    PlayFile(filePath string) error
}

// TTSEngine — TTS 引擎介面
type TTSEngine interface {
    TextToSpeech(text, language string, speed float64) ([]byte, string, error)
    // 回傳：音訊 bytes、副檔名（"mp3"/"aiff"）、error
}
```

### 資料流

**TTS 播放流程**
```
使用者輸入文字
    ↓
TTSEngine.TextToSpeech()       ← Google Cloud / Azure / 本地 say
    ↓ 音訊 bytes
saveTempAudio() → 暫存檔
    ↓
serveAudioFile() → 臨時 HTTP Server（:18765）
    ↓ http://本機IP:18765/audio.mp3
ChromecastPlay() / AirPlay / MiIO
    ↓
智慧音箱播放
```

**音訊檔案直接播放流程**
```
本地 MP3 / WAV / FLAC ...
    ↓
probeAudioFile()               ← 取得格式、大小、時長（ffprobe）
    ↓
serveFileTemporarily()         ← 臨時 HTTP Server（:19876）
    ↓ http://本機IP:19876/audio.mp3
ChromecastPlay() / AirPlay / MiIO play_url
    ↓
智慧音箱串流播放
```

---

## 📄 各檔案說明

### `main.go`
程式進入點，使用 [cobra](https://github.com/spf13/cobra) 建立 CLI，並註冊四個子指令：`speak`、`play`、`list`、`serve`。

---

### `speaker.go`
定義整個系統的核心介面與資料型別。

| 項目 | 說明 |
|------|------|
| `Speaker` 介面 | 所有音箱必須實作的 4 個方法 |
| `AudioPlayer` 介面 | 在 `Speaker` 基礎上加入 `PlayFile()` |
| `Device` 結構 | 裝置資訊（ID、Name、IP、Type、IsOnline）|
| `Config` 結構 | 統一設定（音箱類型、IP、TTS 引擎、語言、速度、音量）|
| `NewSpeaker()` | 工廠方法，根據 `SpeakerType` 建立對應控制器 |

---

### `tts.go`
支援三種 TTS 引擎，統一回傳 `([]byte, string, error)`（音訊資料、副檔名、錯誤）。

| 引擎類型 | struct | 說明 |
|----------|--------|------|
| 雲端 | `GoogleTTSEngine` | Google Cloud Text-to-Speech，回傳 MP3（base64 解碼）|
| 雲端 | `AzureTTSEngine` | Azure Cognitive Services，Neural 聲音，SSML 格式，回傳 MP3 |
| 本地免費 | `LocalTTSEngine` | macOS 用 `say`（Mei-Jia / Ting-Ting）；Linux 用 `espeak-ng`，回傳 AIFF |

`NewTTSEngine(engine, apiKey, region)` 為工廠函式，`engine` 可為 `"google"`、`"azure"`、`"local"`。

---

### `chromecast.go`
**純 Go 實作 Chromecast 通訊協定，不依賴 `catt` 或任何 proto 外部套件。**

#### 協定細節
- 傳輸層：TLS TCP，連接至裝置 IP 的 **8009** port
- 訊息格式：`4 bytes 長度（big-endian）+ protobuf CastMessage binary`
- protobuf 採手工編解碼（`encVarint` / `decVarint` / `protoMsg` / `protoExtract`），零外部依賴

#### 播放流程（`ChromecastPlay`）

```
1. TLS 連線到 <IP>:8009
2. CONNECT  (tp.connection)     → receiver-0
3. LAUNCH   (launcher)          → appId: CC1AD845（Default Media Receiver）
4. 輪詢接收封包，等待 RECEIVER_STATUS → 取得 transportId、sessionId
5. CONNECT  (tp.connection)     → transportId
6. LOAD     (cast.media)        → { contentId: audioURL, contentType, streamType: BUFFERED }
7. 等待 MEDIA_STATUS            → 確認播放開始
```

#### 主要函式

| 函式 | 說明 |
|------|------|
| `ChromecastPlay(ip, url, ct)` | 對外公開的高階播放函式 |
| `newCastConn(ip)` | 建立 TLS 連線（`InsecureSkipVerify: true`）|
| `castConn.send(ns, src, dst, json)` | 組裝並傳送 CastMessage |
| `castConn.recv(timeout)` | 接收並解析一個 CastMessage |
| `waitTransport(...)` | 等待 Media Receiver 啟動，取得 transportId |
| `waitMediaReady(...)` | 等待 MEDIA_STATUS，確認播放成功 |
| `protoMsg(...)` | 手工組裝 protobuf binary（欄位 1~6）|
| `protoExtract(data)` | 從 binary 中抽取 namespace 和 payload |

---

### `google_home.go`
Google Home / Google Nest 控制器。

**播放優先順序：**
```
1. ChromecastPlay（純 Go，最可靠）     ← 有 --google-ip 時優先使用
2. catt（備用，pip install catt）      ← ChromecastPlay 失敗時嘗試
```

**HTTP 串流伺服器：**
在本機啟動臨時 `net/http` 伺服器（預設 `:18765`），同時提供兩個路徑：
- `/audio` — 無副檔名路徑
- `/audio.mp3` — 帶副檔名路徑（Chromecast 需要此路徑識別音訊格式）

**主要函式：**

| 函式 | 說明 |
|------|------|
| `NewGoogleHomeSpeaker(cfg)` | 建立控制器，自動偵測本機 IP |
| `Speak(text)` | TTS → 暫存 → HTTP 伺服器 → Chromecast LOAD |
| `SetVolume(level)` | 呼叫 `catt volume <0-100>`（整數）|
| `GetDevices()` | 呼叫 `catt scan` 列出區域網路裝置 |
| `serveAudioFile(path, ext)` | 啟動 HTTP 伺服器，回傳 (url, contentType, stopFn) |
| `castToDevice(url, ct)` | 先試 `ChromecastPlay`，失敗再試 `catt` |

---

### `apple_home.go`
Apple HomePod 控制器，透過 AirPlay 協定播放。

**播放優先順序：**
```
1. airplay2-sender（開源 AirPlay 2 客戶端）
2. raop-client（brew install raop-client）
3. afplay（macOS 內建，需先在系統偏好設定選 HomePod 輸出）
4. AppleScript + QuickTime Player
```

格式不相容時（FLAC、OGG、OPUS）自動呼叫 `ffmpeg` 轉為 WAV 後再播放。

**裝置探索：** macOS 用 `dns-sd -B _airplay._tcp local`，Linux 用 `avahi-browse`。

---

### `xiaoai.go`
小愛同學（小米 AI 音箱）控制器。

**播放優先順序：**
```
1. MiIO 本地協定（UDP:54321，需裝置 Token）  ← 最快、不依賴外部網路
2. python-miio CLI（miiocli）
3. 小米雲端 API（需帳號登入）
```

**MiIO 協定細節：**

| 項目 | 說明 |
|------|------|
| 傳輸 | UDP，連接裝置 IP 的 **54321** port |
| 加密 | AES-128-CBC；Key = MD5(token)；IV = MD5(Key + token) |
| 封包 | 32 bytes header（含 MD5 checksum）+ 加密 JSON payload |
| TTS 指令 | `{"method": "text_to_speech", "params": ["文字"]}` |
| 播放 URL | `{"method": "play_url", "params": ["http://..."]}` |

**裝置探索：** 廣播 MiIO hello packet（32 bytes 全 0xFF）到 UDP:54321，收集回應。

---

### `play_file.go`
音訊檔案直接播放核心，為三種音箱各自實作 `AudioPlayer.PlayFile()`。

**支援音訊格式：**
`mp3` · `wav` · `flac` · `aac` · `ogg` · `aiff` · `m4a` · `opus`

**主要功能：**

| 函式 | 說明 |
|------|------|
| `probeAudioFile(path)` | 回傳格式、大小、時長（時長需 `ffprobe`）|
| `GoogleHomeSpeaker.PlayFile(path)` | HTTP 伺服器 → ChromecastPlay |
| `AppleHomeSpeaker.PlayFile(path)` | 必要時轉格式 → AirPlay 串流 |
| `XiaoAISpeaker.PlayFile(path)` | MiIO play_url → miiocli → 雲端 |
| `serveFileTemporarily(path, ct, port)` | 通用臨時 HTTP 伺服器（:19876）|
| `resolveAudioFiles(pattern)` | 展開 glob 或目錄，取得音訊檔清單 |
| `NewAudioPlayer(cfg)` | 工廠方法，建立對應音箱的 AudioPlayer |

---

### `commands.go`
CLI 指令定義（使用 cobra）。

#### `speak` — 文字轉語音播放

```
voice-briefing speak [文字] [flags]
```

| Flag | 預設 | 說明 |
|------|------|------|
| `--speaker, -s` | 互動選擇 | `google` \| `apple` \| `xiaoai` |
| `--lang, -l` | `zh-TW` | `zh-TW` \| `zh-CN` \| `en-US` |
| `--tts, -t` | `local` | `local` \| `google` \| `azure` |
| `--speed, -r` | `1.0` | 語速，範圍 0.5 ~ 2.0 |
| `--volume, -v` | `70` | 音量，範圍 0 ~ 100 |
| `--interactive, -i` | `false` | 進入互動模式（連續輸入）|
| `--google-ip` | — | Google Home IP 位址（推薦，跳過 mDNS）|
| `--google-device` | — | Google Home 裝置名稱 |
| `--apple-device` | — | Apple HomePod 裝置名稱 |

#### `play` — 直接播放音訊檔案

```
voice-briefing play [音訊檔案或目錄] [flags]
```

| Flag | 預設 | 說明 |
|------|------|------|
| `--speaker, -s` | 互動選擇 | `google` \| `apple` \| `xiaoai` |
| `--volume, -v` | `70` | 音量，範圍 0 ~ 100 |
| `--google-ip` | — | Google Home IP 位址（推薦）|
| `--google-device` | — | Google Home 裝置名稱 |
| `--apple-device` | — | Apple HomePod 裝置名稱 |

路徑支援 glob（`"/music/*.mp3"`）與目錄（`"/music/"`）批次播放。

#### `list` — 列出可用裝置

```
voice-briefing list
```

同時掃描三種音箱並列出線上裝置（Google 用 catt scan；小米用 UDP 廣播）。

#### `serve` — 啟動 HTTP API 伺服器

```
voice-briefing serve [--port 8080]
```

---

### `server.go`
HTTP REST API 伺服器與嵌入式 Web UI（單一 HTML 字串，無外部靜態資源）。

#### API 端點

| 方法 | 路徑 | 說明 |
|------|------|------|
| `GET` | `/` | Web UI（音箱選擇、TTS 分頁、音訊上傳分頁）|
| `POST` | `/api/speak` | 文字 TTS 播放（同步）|
| `POST` | `/api/play` | 指定伺服器本地路徑播放（非同步）|
| `POST` | `/api/play-upload` | 上傳音訊後播放，`multipart/form-data`（非同步）|
| `GET` | `/api/devices` | 列出所有可用裝置，回傳 JSON |
| `GET` | `/health` | 健康檢查，回傳 `{"status":"ok"}` |

#### `/api/speak` 請求格式（JSON）

```json
{
  "text": "今日股市：加權指數上漲一百二十點",
  "speaker": "google",
  "language": "zh-TW",
  "tts_engine": "local",
  "speed": 1.0,
  "volume": 75
}
```

#### `/api/play` 請求格式（JSON）

```json
{
  "file": "C:/Users/user/music/briefing.mp3",
  "speaker": "google",
  "volume": 75
}
```

#### `/api/play-upload` 請求格式（multipart/form-data）

| 欄位 | 型別 | 說明 |
|------|------|------|
| `file` | 檔案 | 音訊檔案（最大 100 MB）|
| `speaker` | 字串 | `google` \| `apple` \| `xiaoai` |
| `volume` | 整數字串 | 0 ~ 100 |

---

## 🚀 快速開始

### 1. 安裝相依工具

**Google Home（程式內建，無需額外安裝）：**
```bash
# 若內建 Chromecast 失敗，可安裝備用工具
pip install catt
```

**Apple HomePod：**
```bash
brew install raop-client           # macOS / Linux
# 或在 macOS 系統偏好設定 > 聲音 > 輸出 選擇 HomePod
```

**小愛同學：**
```bash
pip install python-miio            # 備用方案
python3 -m miio.extract_tokens     # 從 Mi Home App 備份提取 Token
```

**音訊格式轉換（選用）：**
```bash
brew install ffmpeg     # macOS
apt install ffmpeg      # Ubuntu/Debian
choco install ffmpeg    # Windows
```

### 2. 設定環境變數

```bash
# ── TTS 引擎（擇一，local 完全免費無需設定）──────────────────
export GOOGLE_TTS_API_KEY="AIza..."
export AZURE_TTS_API_KEY="abc123..."
export AZURE_TTS_REGION="eastasia"

# ── Google Home ───────────────────────────────────────────────
# 在 Google Home App > 裝置 > 齒輪 > 裝置資訊 查看 IP
export GOOGLE_DEVICE_IP="192.168.1.100"   # 強烈建議設定，可跳過 mDNS
export GOOGLE_DEVICE_NAME="客廳音箱"       # 替代（依賴 mDNS）

# ── Apple HomePod ─────────────────────────────────────────────
export APPLE_DEVICE_NAME="HomePod mini"

# ── 小愛同學 ──────────────────────────────────────────────────
export XIAOAI_DEVICE_IP="192.168.1.101"
export XIAOAI_DEVICE_TOKEN="32位十六進位Token"
export XIAOAI_MI_SERVER="sg"              # 台灣/東南亞用 sg；中國大陸用 cn
export XIAOAI_MI_ID="your@email.com"      # 雲端備用
export XIAOAI_PASSWORD="your-password"   # 雲端備用
```

### 3. 編譯與執行

```bash
cd voice_briefing
go mod tidy
go build -o voice-briefing .        # macOS / Linux
go build -o voice-briefing.exe .    # Windows

# 或直接執行（不編譯）
go run ./... play briefing.mp3 --speaker google --google-ip 192.168.1.100
```

---

## 💻 使用範例

### 文字轉語音

```bash
# Google Home + 本地 TTS（免 API Key）
voice-briefing speak --speaker google --google-ip 192.168.1.100 \
  "今日股市：加權指數收漲零點八%"

# Apple HomePod + Azure Neural TTS
voice-briefing speak --speaker apple --tts azure --lang zh-TW \
  "明天早上十點有重要會議，請準時出席"

# 小愛同學
voice-briefing speak --speaker xiaoai "今天天氣：台北多雲，最高溫 28 度"

# 互動模式
voice-briefing speak --interactive --speaker google --google-ip 192.168.1.100
```

### 直接播放音訊檔案

```bash
# 播放單一 MP3
voice-briefing play briefing.mp3 --speaker google --google-ip 192.168.1.100

# 指定音量
voice-briefing play news.wav --speaker apple --volume 85

# 批次播放整個目錄
voice-briefing play /music/ --speaker google --google-ip 192.168.1.100

# 批次播放 glob（注意引號）
voice-briefing play "/music/*.mp3" --speaker google
```

### HTTP API

```bash
# 啟動伺服器
voice-briefing serve --port 8080

# TTS 播放
curl -X POST http://localhost:8080/api/speak \
  -H "Content-Type: application/json" \
  -d '{"text":"今日簡報","speaker":"google","language":"zh-TW","tts_engine":"local","speed":1.0,"volume":75}'

# 本地路徑播放
curl -X POST http://localhost:8080/api/play \
  -H "Content-Type: application/json" \
  -d '{"file":"/home/user/news.mp3","speaker":"google","volume":75}'

# 上傳並播放
curl -X POST http://localhost:8080/api/play-upload \
  -F "file=@briefing.mp3" -F "speaker=apple" -F "volume=80"
```

---

## 🔧 各平台注意事項

### Windows

- **Google Home**：強烈建議用 `--google-ip` 指定 IP，因為 Windows 防火牆預設封鎖 mDNS（UDP 5353），會導致自動掃描失敗。
  ```powershell
  # 開放 mDNS（選用）
  netsh advfirewall firewall add rule name="mDNS" protocol=UDP dir=in localport=5353 action=allow
  ```
- **本地 TTS**：Windows 無 `say` 或 `espeak`，請改用 `--tts google` 或 `--tts azure`。

### macOS

- **Apple HomePod**：最佳方式是在「系統偏好設定 > 聲音 > 輸出」選擇 HomePod，程式會自動用 `afplay` 透過 AirPlay 路由播放。
- **本地 TTS**：內建 `say` 指令，中文語音為 `Mei-Jia`（繁體中文）或 `Ting-Ting`（簡體中文）。

### Linux

- **本地 TTS**：`apt install espeak-ng`。
- **Apple HomePod**：使用 `raop-client` 或透過 `avahi-browse` 探索裝置。

---

## 📊 TTS 引擎比較

| 引擎 | 費用 | 中文音質 | 需 API Key | 離線可用 |
|------|------|---------|-----------|---------|
| `local`（macOS say） | 完全免費 | 中等 | 否 | ✅ |
| `local`（Linux espeak-ng） | 完全免費 | 較差 | 否 | ✅ |
| `google`（Cloud TTS） | 免費額度 100 萬字/月 | 優 | 是 | ❌ |
| `azure`（Neural TTS） | 免費額度 50 萬字/月 | 優 | 是 | ❌ |

---

## 📦 相依套件總覽

| 套件 | 類型 | 必要 | 用途 |
|------|------|------|------|
| `github.com/spf13/cobra` | Go module | ✅ | CLI 框架 |
| `catt`（pip） | 外部工具 | 選用 | Google Home 備用控制、音量設定 |
| `raop-client`（brew） | 外部工具 | 選用 | Apple HomePod AirPlay |
| `python-miio`（pip） | 外部工具 | 選用 | 小愛同學備用控制、Token 提取 |
| `ffmpeg` | 外部工具 | 選用 | 音訊格式轉換、時長探測（ffprobe） |

> Google Home 播放已內建純 Go Chromecast 實作，**不強制需要 catt**。
> 若 `catt` 未安裝，`SetVolume` 和 `GetDevices` 功能將無法使用，但播放不受影響。
