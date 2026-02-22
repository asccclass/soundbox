package main

import (
	"fmt"
)

// Speaker 是所有智慧音箱的通用介面
type Speaker interface {
	Name() string
	Speak(text string) error
	SetVolume(level int) error
	GetDevices() ([]Device, error)
}

// Device 代表一個智慧音箱裝置
type Device struct {
	ID       string
	Name     string
	IP       string
	Type     string
	IsOnline bool
}

// SpeakerType 音箱類型
type SpeakerType string

const (
	GoogleHome SpeakerType = "google"
	AppleHome  SpeakerType = "apple"
	XiaoAI     SpeakerType = "xiaoai"
)

// Config 設定結構
type Config struct {
	SpeakerType SpeakerType

	// Google Home
	GoogleDeviceIP   string
	GoogleDeviceName string

	// Apple HomePod
	AppleDeviceName string // AirPlay 裝置名稱

	// 小愛同學
	XiaoAIMiID      string // 小米帳號
	XiaoAIPassword  string // 小米密碼
	XiaoAIDeviceID  string // 裝置 ID

	// TTS 設定
	TTSEngine   string // "google", "azure", "local"
	Language    string // "zh-TW", "zh-CN", "en-US"
	VoiceSpeed  float64
	Volume      int
}

// NewSpeaker 工廠方法：根據設定建立對應的音箱控制器
func NewSpeaker(cfg Config) (Speaker, error) {
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
