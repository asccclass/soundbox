package main

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "voice-briefing",
	Short: "語音簡報工具 - 透過智慧音箱播放 TTS 訊息",
	Long: `Voice Briefing - 支援 Google Home、Apple HomePod、小愛同學
將文字透過 TTS 轉換後傳送至您選擇的智慧音箱播放。`,
	SilenceUsage: true,
}

func main() {
	// 嘗試載入環境變數檔案，忽略找不到檔案的錯誤
	_ = godotenv.Load("envfile", ".env")

	rootCmd.AddCommand(speakCmd)
	rootCmd.AddCommand(playCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(serveCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
