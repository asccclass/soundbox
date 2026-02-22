package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TTSEngine TTS 轉換引擎介面，回傳音訊 bytes、副檔名、error
type TTSEngine interface {
	TextToSpeech(text, language string, speed float64) ([]byte, string, error)
}

// NewTTSEngine 工廠：根據 engine 字串建立對應引擎
func NewTTSEngine(engine, apiKey, region string) (TTSEngine, error) {
	switch engine {
	case "google":
		if apiKey == "" {
			return nil, fmt.Errorf("Google TTS 需要 API Key，請設定環境變數 GOOGLE_TTS_API_KEY")
		}
		return &GoogleTTSEngine{APIKey: apiKey}, nil
	case "azure":
		if apiKey == "" || region == "" {
			return nil, fmt.Errorf("Azure TTS 需要 AZURE_TTS_API_KEY 和 AZURE_TTS_REGION 環境變數")
		}
		return &AzureTTSEngine{APIKey: apiKey, Region: region}, nil
	default: // "local" 或未指定
		return &LocalTTSEngine{}, nil
	}
}

// ─── Google Cloud TTS ─────────────────────────────────────────────────────────

type GoogleTTSEngine struct{ APIKey string }

func (g *GoogleTTSEngine) TextToSpeech(text, language string, speed float64) ([]byte, string, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"input": map[string]string{"text": text},
		"voice": map[string]string{
			"languageCode": language,
			"ssmlGender":   "NEUTRAL",
		},
		"audioConfig": map[string]interface{}{
			"audioEncoding": "MP3",
			"speakingRate":  speed,
		},
	})

	apiURL := "https://texttospeech.googleapis.com/v1/text:synthesize?key=" + g.APIKey
	resp, err := http.Post(apiURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, "", fmt.Errorf("Google TTS 請求失敗: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Google TTS 錯誤 %d: %s", resp.StatusCode, respBytes)
	}

	var result struct {
		AudioContent string `json:"audioContent"`
	}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, "", fmt.Errorf("解析 Google TTS 回應失敗: %w", err)
	}

	audio, err := base64.StdEncoding.DecodeString(result.AudioContent)
	return audio, "mp3", err
}

// ─── Azure Cognitive Services TTS ────────────────────────────────────────────

type AzureTTSEngine struct{ APIKey, Region string }

func (a *AzureTTSEngine) TextToSpeech(text, language string, speed float64) ([]byte, string, error) {
	voiceMap := map[string]string{
		"zh-TW": "zh-TW-HsiaoChenNeural",
		"zh-CN": "zh-CN-XiaoxiaoNeural",
		"en-US": "en-US-JennyNeural",
	}
	voice := voiceMap[language]
	if voice == "" {
		voice = "zh-TW-HsiaoChenNeural"
	}

	ssml := fmt.Sprintf(`<speak version='1.0' xml:lang='%s'>
  <voice name='%s'><prosody rate='%+.0f%%'>%s</prosody></voice>
</speak>`, language, voice, (speed-1)*100, text)

	apiURL := fmt.Sprintf("https://%s.tts.speech.microsoft.com/cognitiveservices/v1", a.Region)
	req, _ := http.NewRequest("POST", apiURL, strings.NewReader(ssml))
	req.Header.Set("Ocp-Apim-Subscription-Key", a.APIKey)
	req.Header.Set("Content-Type", "application/ssml+xml")
	req.Header.Set("X-Microsoft-OutputFormat", "audio-16khz-128kbitrate-mono-mp3")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("Azure TTS 請求失敗: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("Azure TTS 錯誤 %d: %s", resp.StatusCode, b)
	}

	data, err := io.ReadAll(resp.Body)
	return data, "mp3", err
}

// ─── Local TTS (Windows SAPI / macOS say / Linux espeak-ng) ───────────────────

type LocalTTSEngine struct{}

func (l *LocalTTSEngine) TextToSpeech(text, language string, speed float64) ([]byte, string, error) {
	switch {
	case commandExists("say"): // macOS 內建
		return l.ttsWithSay(text, language, speed)

	case commandExists("powershell") || commandExists("powershell.exe"): // Windows SAPI
		return l.ttsWithSAPI(text, language, speed)

	case commandExists("espeak-ng"): // Linux
		return l.ttsWithEspeak("espeak-ng", text, language, speed)

	case commandExists("espeak"):
		return l.ttsWithEspeak("espeak", text, language, speed)

	default:
		return nil, "", fmt.Errorf(
			"未找到本地 TTS 工具\n" +
				"  Windows: 請確認 powershell.exe 可執行（應已內建）\n" +
				"  macOS:   內建 say 指令，應自動偵測\n" +
				"  Linux:   apt install espeak-ng")
	}
}

// ttsWithSay macOS 內建 say 指令，輸出 AIFF
func (l *LocalTTSEngine) ttsWithSay(text, language string, speed float64) ([]byte, string, error) {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("vb_%d.aiff", time.Now().UnixNano()))
	defer os.Remove(tmp)

	voice := map[string]string{
		"zh-TW": "Mei-Jia",
		"zh-CN": "Ting-Ting",
		"en-US": "Samantha",
	}[language]
	if voice == "" {
		voice = "Mei-Jia"
	}
	rate := fmt.Sprintf("%d", int(speed*180))
	cmd := exec.Command("say", "-v", voice, "-r", rate, "-o", tmp, "--", text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("say 失敗: %w\n%s", err, out)
	}
	data, err := os.ReadFile(tmp)
	return data, "aiff", err
}

// ttsWithSAPI Windows 內建 SAPI，透過 PowerShell SpeechSynthesizer 輸出 WAV
func (l *LocalTTSEngine) ttsWithSAPI(text, language string, speed float64) ([]byte, string, error) {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("vb_%d.wav", time.Now().UnixNano()))
	defer os.Remove(tmp)

	// speed 0.5~2.0 對應 SAPI rate -5~5（0 為正常，範圍 -10~10）
	sapiRate := int((speed - 1.0) * 5)

	// 逸出文字避免 PowerShell 注入
	safeText := strings.ReplaceAll(text, "'", "''")
	safeText = strings.ReplaceAll(safeText, "`", "``")
	// PowerShell 路徑用正斜線避免跳脫問題
	safeTmp := strings.ReplaceAll(tmp, `\`, `/`)

	// 依語言選擇聲音（Windows 內建，SelectVoiceByHints 失敗時靜默降回預設聲音）
	voiceHint := ""
	switch language {
	case "zh-TW":
		voiceHint = `try { $s.SelectVoiceByHints('NotSet','NotSet',0,[System.Globalization.CultureInfo]'zh-TW') } catch {}`
	case "zh-CN":
		voiceHint = `try { $s.SelectVoiceByHints('NotSet','NotSet',0,[System.Globalization.CultureInfo]'zh-CN') } catch {}`
	case "en-US":
		voiceHint = `try { $s.SelectVoiceByHints('Female','NotSet',0,[System.Globalization.CultureInfo]'en-US') } catch {}`
	}

	script := fmt.Sprintf(
		`Add-Type -AssemblyName System.Speech;`+
			`$s=New-Object System.Speech.Synthesis.SpeechSynthesizer;`+
			`%s`+
			`$s.Rate=%d;`+
			`$s.SetOutputToWaveFile('%s');`+
			`$s.Speak('%s');`+
			`$s.Dispose()`,
		voiceHint, sapiRate, safeTmp, safeText,
	)

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("Windows SAPI TTS 失敗: %w\n%s", err, out)
	}

	data, err := os.ReadFile(tmp)
	if err != nil {
		return nil, "", fmt.Errorf("讀取 WAV 失敗（PowerShell 可能未產生輸出）: %w", err)
	}
	return data, "wav", nil
}

// ttsWithEspeak Linux espeak / espeak-ng，輸出 WAV
func (l *LocalTTSEngine) ttsWithEspeak(bin, text, language string, speed float64) ([]byte, string, error) {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("vb_%d.wav", time.Now().UnixNano()))
	defer os.Remove(tmp)

	lang := strings.ToLower(strings.ReplaceAll(language, "-", "_"))
	s := fmt.Sprintf("%d", int(speed*150))
	cmd := exec.Command(bin, "-v", lang, "-s", s, "-w", tmp, text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("%s 失敗: %w\n%s", bin, err, out)
	}
	data, err := os.ReadFile(tmp)
	return data, "wav", err
}

// ─── 工具函式 ──────────────────────────────────────────────────────────────────

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func saveTempAudio(data []byte, ext string) (string, error) {
	f, err := os.CreateTemp("", fmt.Sprintf("vb_audio_*.%s", ext))
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	f.Close()
	return f.Name(), err
}
