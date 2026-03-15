package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// ─── speak 指令 ──────────────────────────────────────────────────────────────

var speakCmd = &cobra.Command{
	Use:   "speak [文字]",
	Short: "將文字透過 TTS 播放到選定的音箱",
	Long: `將文字轉換為語音並播放到智慧音箱。
	
範例:
  voice-briefing speak --speaker google "今天天氣晴朗，股市上漲"
  voice-briefing speak --speaker apple --lang zh-TW "明天開會時間是早上十點"
  voice-briefing speak --speaker xiaoai --tts azure "今日簡報：..."
  voice-briefing speak --interactive   # 互動模式`,
	RunE: func(cmd *cobra.Command, args []string) error {
		interactive, _ := cmd.Flags().GetBool("interactive")
		if interactive {
			return runInteractiveMode(cmd)
		}

		if len(args) == 0 {
			return fmt.Errorf("請提供要播放的文字，或使用 --interactive 進入互動模式")
		}
		text := strings.Join(args, " ")
		return runSpeak(cmd, text)
	},
}

func init() {
	speakCmd.Flags().StringP("speaker", "s", "", "音箱類型: google | apple | xiaoai")
	speakCmd.Flags().StringP("lang", "l", "zh-TW", "語言: zh-TW | zh-CN | en-US")
	speakCmd.Flags().StringP("tts", "t", "local", "TTS 引擎: local | google | azure")
	speakCmd.Flags().Float64P("speed", "r", 1.0, "語速 (0.5~2.0)")
	speakCmd.Flags().IntP("volume", "v", 50, "音量 (0~100)")
	speakCmd.Flags().BoolP("interactive", "i", false, "互動模式（多次輸入）")
	speakCmd.Flags().String("google-device", "", "Google Home 裝置名稱")
	speakCmd.Flags().String("google-ip", "", "Google Home IP 位址")
	speakCmd.Flags().String("apple-device", "", "Apple HomePod 裝置名稱")
	speakCmd.Flags().String("xiaoai-device-id", "", "小愛音箱裝置 ID")
}

func runSpeak(cmd *cobra.Command, text string) error {
	speakerType, _ := cmd.Flags().GetString("speaker")
	lang, _ := cmd.Flags().GetString("lang")
	ttsEngine, _ := cmd.Flags().GetString("tts")
	speed, _ := cmd.Flags().GetFloat64("speed")
	volume, _ := cmd.Flags().GetInt("volume")
	googleDevice, _ := cmd.Flags().GetString("google-device")
	googleIP, _ := cmd.Flags().GetString("google-ip")
	appleDevice, _ := cmd.Flags().GetString("apple-device")
	xiaoaiDeviceID, _ := cmd.Flags().GetString("xiaoai-device-id")

	// 未指定音箱則互動選擇
	if speakerType == "" {
		chosen, err := chooseSpeakerInteractive()
		if err != nil {
			return err
		}
		speakerType = chosen
	}

	cfg := Config{
		SpeakerType:      SpeakerType(speakerType),
		Language:         lang,
		TTSEngine:        ttsEngine,
		VoiceSpeed:       speed,
		Volume:           volume,
		GoogleDeviceName: googleDevice,
		GoogleDeviceIP:   googleIP,
		AppleDeviceName:  appleDevice,
		XiaoAIDeviceID:   xiaoaiDeviceID,
		XiaoAIMiID:       os.Getenv("XIAOAI_MI_ID"),
		XiaoAIPassword:   os.Getenv("XIAOAI_PASSWORD"),
	}

	// 從環境變數補充設定
	if cfg.GoogleDeviceIP == "" {
		cfg.GoogleDeviceIP = os.Getenv("GOOGLE_DEVICE_IP")
	}
	if cfg.GoogleDeviceName == "" {
		cfg.GoogleDeviceName = os.Getenv("GOOGLE_DEVICE_NAME")
	}
	if cfg.AppleDeviceName == "" {
		cfg.AppleDeviceName = os.Getenv("APPLE_DEVICE_NAME")
	}
	if cfg.XiaoAIDeviceID == "" {
		cfg.XiaoAIDeviceID = os.Getenv("XIAOAI_DEVICE_ID")
	}

	speaker, err := NewSpeaker(cfg)
	if err != nil {
		return fmt.Errorf("建立音箱控制器失敗: %w", err)
	}

	fmt.Printf("📡 目標音箱: %s\n", speaker.Name())
	fmt.Printf("🗣️  語言: %s | TTS: %s | 速度: %.1f | 音量: %d\n",
		lang, ttsEngine, speed, volume)

	if err := speaker.SetVolume(volume); err != nil {
		fmt.Printf("⚠️  設定音量失敗（繼續播放）: %v\n", err)
	}

	return speaker.Speak(text)
}

func runInteractiveMode(cmd *cobra.Command) error {
	fmt.Println("🎙️  Voice Briefing 互動模式（輸入 'quit' 結束）")
	fmt.Println(strings.Repeat("─", 50))

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n📝 輸入文字: ")
		if !scanner.Scan() {
			break
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "quit" || text == "exit" || text == "q" {
			fmt.Println("👋 再見！")
			break
		}
		if text == "" {
			continue
		}
		if err := runSpeak(cmd, text); err != nil {
			fmt.Printf("❌ 錯誤: %v\n", err)
		}
	}
	return nil
}

// ─── list 指令 ───────────────────────────────────────────────────────────────

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "列出可用的智慧音箱裝置",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("🔍 掃描智慧音箱裝置...")
		fmt.Println(strings.Repeat("─", 50))

		speakerTypes := []SpeakerType{GoogleHome, AppleHome, XiaoAI}
		cfg := Config{
			TTSEngine: "local",
			Language:  "zh-TW",
		}

		for _, st := range speakerTypes {
			cfg.SpeakerType = st
			speaker, err := NewSpeaker(cfg)
			if err != nil {
				fmt.Printf("⚠️  %s: %v\n", st, err)
				continue
			}

			fmt.Printf("\n📡 %s:\n", speaker.Name())
			devices, err := speaker.GetDevices()
			if err != nil {
				fmt.Printf("   ⚠️  掃描失敗: %v\n", err)
				continue
			}
			if len(devices) == 0 {
				fmt.Println("   （未找到裝置）")
				continue
			}
			for _, d := range devices {
				status := "🟢"
				if !d.IsOnline {
					status = "🔴"
				}
				fmt.Printf("   %s %s (IP: %s, ID: %s)\n", status, d.Name, d.IP, d.ID)
			}
		}
		return nil
	},
}

// ─── serve 指令（HTTP API 模式）──────────────────────────────────────────────

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "啟動 HTTP API 伺服器，提供 REST 介面控制音箱",
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		return runHTTPServer(port)
	},
}

func init() {
	serveCmd.Flags().IntP("port", "p", 8080, "HTTP 伺服器 port")
}

// ─── 互動式選擇音箱 ───────────────────────────────────────────────────────────

func chooseSpeakerInteractive() (string, error) {
	fmt.Println("\n🔊 請選擇音箱:")
	fmt.Println("  1. Google Home")
	fmt.Println("  2. Apple HomePod")
	fmt.Println("  3. 小愛同學 (XiaoAI)")
	fmt.Print("\n請輸入選項 (1-3): ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return "", fmt.Errorf("讀取輸入失敗")
	}

	choice := strings.TrimSpace(scanner.Text())
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > 3 {
		return "", fmt.Errorf("無效選項: %s（請輸入 1-3）", choice)
	}

	speakers := []string{"google", "apple", "xiaoai"}
	return speakers[n-1], nil
}

// ─── play 指令（直接播放音訊檔案）────────────────────────────────────────────

var playCmd = &cobra.Command{
	Use:   "play [音訊檔案或目錄]",
	Short: "直接播放 MP3/WAV/FLAC 等音訊檔案到智慧音箱",
	Long: `直接串流本地音訊檔案到智慧音箱，不需要 TTS 轉換。

支援格式: mp3, wav, flac, aac, ogg, aiff, m4a, opus

範例:
  voice-briefing play briefing.mp3 --speaker google
  voice-briefing play news.wav --speaker apple --volume 80
  voice-briefing play podcast.mp3 --speaker xiaoai
  voice-briefing play /music/*.mp3 --speaker google   # 批次播放
  voice-briefing play /music/ --speaker apple          # 播放整個目錄`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		speakerType, _ := cmd.Flags().GetString("speaker")
		volume, _ := cmd.Flags().GetInt("volume")
		googleDevice, _ := cmd.Flags().GetString("google-device")
		googleIP, _ := cmd.Flags().GetString("google-ip")
		appleDevice, _ := cmd.Flags().GetString("apple-device")

		if speakerType == "" {
			chosen, err := chooseSpeakerInteractive()
			if err != nil {
				return err
			}
			speakerType = chosen
		}

		cfg := Config{
			SpeakerType:      SpeakerType(speakerType),
			Language:         "zh-TW",
			TTSEngine:        "local",
			VoiceSpeed:       1.0,
			Volume:           volume,
			GoogleDeviceName: googleDevice,
			GoogleDeviceIP:   googleIP,
			AppleDeviceName:  appleDevice,
			XiaoAIDeviceID:   os.Getenv("XIAOAI_DEVICE_ID"),
			XiaoAIMiID:       os.Getenv("XIAOAI_MI_ID"),
			XiaoAIPassword:   os.Getenv("XIAOAI_PASSWORD"),
		}
		if cfg.GoogleDeviceIP == "" {
			cfg.GoogleDeviceIP = os.Getenv("GOOGLE_DEVICE_IP")
		}
		if cfg.GoogleDeviceName == "" {
			cfg.GoogleDeviceName = os.Getenv("GOOGLE_DEVICE_NAME")
		}
		if cfg.AppleDeviceName == "" {
			cfg.AppleDeviceName = os.Getenv("APPLE_DEVICE_NAME")
		}

		player, err := NewAudioPlayer(cfg)
		if err != nil {
			return fmt.Errorf("建立播放器失敗: %w", err)
		}

		fmt.Printf("📡 目標音箱: %s | 音量: %d\n", player.Name(), volume)

		if err := player.SetVolume(volume); err != nil {
			fmt.Printf("⚠️  設定音量失敗（繼續播放）: %v\n", err)
		}

		// 支援多個路徑引數
		for _, pattern := range args {
			files, err := resolveAudioFiles(pattern)
			if err != nil {
				fmt.Printf("⚠️  %v\n", err)
				continue
			}

			for i, file := range files {
				if len(files) > 1 {
					fmt.Printf("\n[%d/%d] ", i+1, len(files))
				}
				if err := player.PlayFile(file); err != nil {
					fmt.Printf("❌ 播放失敗 %s: %v\n", file, err)
				}
			}
		}
		return nil
	},
}

func init() {
	playCmd.Flags().StringP("speaker", "s", "", "音箱類型: google | apple | xiaoai")
	playCmd.Flags().IntP("volume", "v", 70, "音量 (0~100)")
	playCmd.Flags().String("google-device", "", "Google Home 裝置名稱")
	playCmd.Flags().String("google-ip", "", "Google Home IP 位址")
	playCmd.Flags().String("apple-device", "", "Apple HomePod 裝置名稱")
}
