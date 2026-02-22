package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// AppleHomeSpeaker 透過 AirPlay 協定控制 Apple HomePod
type AppleHomeSpeaker struct {
	cfg Config
	tts TTSEngine
}

func NewAppleHomeSpeaker(cfg Config) (*AppleHomeSpeaker, error) {
	ttsEngine, err := NewTTSEngine(cfg.TTSEngine,
		os.Getenv("GOOGLE_TTS_API_KEY"),
		os.Getenv("AZURE_TTS_REGION"))
	if err != nil {
		return nil, err
	}
	return &AppleHomeSpeaker{cfg: cfg, tts: ttsEngine}, nil
}

func (a *AppleHomeSpeaker) Name() string { return "Apple HomePod" }

func (a *AppleHomeSpeaker) Speak(text string) error {
	fmt.Printf("🔊 [Apple HomePod] TTS 轉換中: %q\n", truncate(text, 50))

	audioData, ext, err := a.tts.TextToSpeech(text, a.cfg.Language, a.cfg.VoiceSpeed)
	if err != nil {
		return fmt.Errorf("TTS 失敗: %w", err)
	}

	audioFile, err := saveTempAudio(audioData, ext)
	if err != nil {
		return fmt.Errorf("儲存音訊失敗: %w", err)
	}
	defer os.Remove(audioFile)

	// 嘗試轉 WAV（AirPlay 相容格式）
	wavFile, err := convertToWAV(audioFile, ext)
	if err != nil {
		wavFile = audioFile
	} else {
		defer os.Remove(wavFile)
	}

	return a.playViaAirPlay(wavFile)
}

func (a *AppleHomeSpeaker) SetVolume(level int) error {
	if commandExists("airplay-speaker") {
		return exec.Command("airplay-speaker",
			"--device", a.cfg.AppleDeviceName,
			"--volume", fmt.Sprintf("%d", level)).Run()
	}
	return fmt.Errorf("設定 HomePod 音量需要安裝 airplay-speaker")
}

func (a *AppleHomeSpeaker) GetDevices() ([]Device, error) {
	var out []byte
	var err error

	if commandExists("dns-sd") {
		cmd := exec.Command("dns-sd", "-B", "_airplay._tcp", "local")
		done := make(chan error, 1)
		go func() { out, err = cmd.Output(); done <- err }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		}
	} else if commandExists("avahi-browse") {
		out, err = exec.Command("avahi-browse", "-t", "_airplay._tcp").Output()
		if err != nil {
			return nil, err
		}
	}

	return parseAirPlayDevices(string(out)), nil
}

func (a *AppleHomeSpeaker) playViaAirPlay(audioFile string) error {
	deviceName := a.cfg.AppleDeviceName
	if deviceName == "" {
		deviceName = "HomePod"
	}

	// 方法 1: airplay2-sender
	if commandExists("airplay2-sender") {
		out, err := exec.Command("airplay2-sender", "--host", deviceName, "--file", audioFile).CombinedOutput()
		if err != nil {
			return fmt.Errorf("airplay2-sender 失敗: %w\n%s", err, out)
		}
		fmt.Println("✅ [Apple HomePod] 播放成功")
		return nil
	}

	// 方法 2: raop-client
	if commandExists("raop-client") {
		out, err := exec.Command("raop-client", "--host", deviceName, audioFile).CombinedOutput()
		if err != nil {
			return fmt.Errorf("raop-client 失敗: %w\n%s", err, out)
		}
		fmt.Println("✅ [Apple HomePod] 播放成功")
		return nil
	}

	// 方法 3: macOS afplay（需手動在系統偏好設定選 HomePod 為輸出）
	if commandExists("afplay") {
		fmt.Println("⚠️  使用 afplay（請先在 macOS 系統偏好設定 > 聲音 > 輸出 選擇 HomePod）")
		out, err := exec.Command("afplay", audioFile).CombinedOutput()
		if err != nil {
			return fmt.Errorf("afplay 失敗: %w\n%s", err, out)
		}
		fmt.Println("✅ [Apple HomePod] 播放成功")
		return nil
	}

	// 方法 4: AppleScript + QuickTime
	if commandExists("osascript") {
		return a.playViaAppleScript(audioFile)
	}

	return fmt.Errorf("未找到 AirPlay 工具。請安裝 raop-client (brew install raop-client) 或在 macOS 系統設定選擇 HomePod 輸出")
}

func (a *AppleHomeSpeaker) playViaAppleScript(audioFile string) error {
	script := fmt.Sprintf(`
tell application "QuickTime Player"
	set theFile to POSIX file "%s"
	open theFile
	play document 1
	repeat while (playing of document 1)
		delay 0.5
	end repeat
	close document 1
end tell`, audioFile)

	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("AppleScript 失敗: %w\n%s", err, out)
	}
	fmt.Println("✅ [Apple HomePod] 透過 QuickTime 播放成功")
	return nil
}

func convertToWAV(inputFile, ext string) (string, error) {
	if ext == "wav" {
		return inputFile, nil
	}
	if !commandExists("ffmpeg") {
		return "", fmt.Errorf("需要 ffmpeg 進行格式轉換")
	}
	wavFile := inputFile[:len(inputFile)-len(ext)] + "wav"
	if err := exec.Command("ffmpeg", "-y", "-i", inputFile, "-ar", "44100", "-ac", "2", wavFile).Run(); err != nil {
		return "", err
	}
	return wavFile, nil
}

func parseAirPlayDevices(output string) []Device {
	var devices []Device
	for i, line := range splitLines(output) {
		if len(line) > 0 && (containsStr(line, "_airplay") || containsStr(line, "HomePod") || containsStr(line, "AirPlay")) {
			name := line
			if len(name) > 40 {
				name = name[:40]
			}
			devices = append(devices, Device{
				ID: fmt.Sprintf("apple-%d", i), Name: name, Type: "Apple HomePod", IsOnline: true,
			})
		}
	}
	return devices
}

func containsStr(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
