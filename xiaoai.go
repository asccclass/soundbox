package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// XiaoAISpeaker 透過 MiIO 協定和小米雲端 API 控制小愛音箱
type XiaoAISpeaker struct {
	cfg      Config
	tts      TTSEngine
	miToken  string
	miServer string
}

func NewXiaoAISpeaker(cfg Config) (*XiaoAISpeaker, error) {
	ttsEngine, err := NewTTSEngine(cfg.TTSEngine,
		os.Getenv("GOOGLE_TTS_API_KEY"),
		os.Getenv("AZURE_TTS_REGION"))
	if err != nil {
		return nil, err
	}
	miServer := os.Getenv("XIAOAI_MI_SERVER")
	if miServer == "" {
		miServer = "cn"
	}
	return &XiaoAISpeaker{cfg: cfg, tts: ttsEngine, miServer: miServer}, nil
}

func (x *XiaoAISpeaker) Name() string { return "小愛同學" }

func (x *XiaoAISpeaker) Speak(text string) error {
	fmt.Printf("🔊 [小愛同學] 準備播放: %q\n", truncate(text, 50))

	// 方法 1: MiIO 本地協定（需要 Token）
	if x.getDeviceIP() != "" {
		if err := x.speakViaMiIO(text); err == nil {
			return nil
		} else {
			fmt.Printf("⚠️  MiIO 本地失敗，嘗試其他方式: %v\n", err)
		}
	}

	// 方法 2: 小米雲端 API
	if x.cfg.XiaoAIMiID != "" {
		return x.speakViaCloud(text)
	}

	// 方法 3: python-miio CLI
	if commandExists("miiocli") {
		return x.speakViaMiIOCli(text)
	}

	return fmt.Errorf("小愛同學需設定 XIAOAI_DEVICE_IP + XIAOAI_DEVICE_TOKEN，或 XIAOAI_MI_ID + XIAOAI_PASSWORD")
}

func (x *XiaoAISpeaker) SetVolume(level int) error {
	return x.sendMiIOCommand("set_volume", []interface{}{level})
}

func (x *XiaoAISpeaker) GetDevices() ([]Device, error) {
	return x.discoverMiIODevices()
}

// ─── MiIO 本地協定 ────────────────────────────────────────────────────────────

func (x *XiaoAISpeaker) speakViaMiIO(text string) error {
	deviceToken := os.Getenv("XIAOAI_DEVICE_TOKEN")
	if deviceToken == "" {
		return fmt.Errorf("需要 XIAOAI_DEVICE_TOKEN")
	}
	payload := map[string]interface{}{
		"id":     1,
		"method": "text_to_speech",
		"params": []string{text},
	}
	return x.sendMiIOPacket(x.getDeviceIP(), deviceToken, payload)
}

func (x *XiaoAISpeaker) sendMiIOCommand(method string, params interface{}) error {
	deviceIP := x.getDeviceIP()
	deviceToken := os.Getenv("XIAOAI_DEVICE_TOKEN")
	if deviceIP == "" || deviceToken == "" {
		return fmt.Errorf("需要 XIAOAI_DEVICE_IP 和 XIAOAI_DEVICE_TOKEN")
	}
	payload := map[string]interface{}{"id": 1, "method": method, "params": params}
	return x.sendMiIOPacket(deviceIP, deviceToken, payload)
}

func (x *XiaoAISpeaker) sendMiIOPacket(deviceIP, token string, payload interface{}) error {
	tokenBytes, err := hex.DecodeString(token)
	if err != nil {
		return fmt.Errorf("Token 格式錯誤（需 32 位 hex）: %w", err)
	}

	payloadJSON, _ := json.Marshal(payload)
	encrypted, err := miioEncrypt(tokenBytes, payloadJSON)
	if err != nil {
		return fmt.Errorf("加密失敗: %w", err)
	}

	stamp := uint32(time.Now().Unix())
	packet := buildMiIOPacket(tokenBytes, stamp, encrypted)

	conn, err := net.DialTimeout("udp", fmt.Sprintf("%s:54321", deviceIP), 5*time.Second)
	if err != nil {
		return fmt.Errorf("連接裝置失敗: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := conn.Write(packet); err != nil {
		return fmt.Errorf("傳送失敗: %w", err)
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("接收回應失敗: %w", err)
	}
	fmt.Printf("✅ [小愛同學] MiIO 回應: %d bytes\n", n)
	return nil
}

func miioEncrypt(token, data []byte) ([]byte, error) {
	key := md5bytes(token)
	iv := md5bytes(append(key, token...))

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padLen := aes.BlockSize - len(data)%aes.BlockSize
	padded := append(data, bytes.Repeat([]byte{byte(padLen)}, padLen)...)

	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, padded)
	return encrypted, nil
}

func buildMiIOPacket(token []byte, stamp uint32, data []byte) []byte {
	length := uint16(32 + len(data))
	pkt := make([]byte, 32+len(data))
	pkt[0] = 0x21
	pkt[1] = 0x31
	binary.BigEndian.PutUint16(pkt[2:], length)
	binary.BigEndian.PutUint32(pkt[4:], 0)
	binary.BigEndian.PutUint32(pkt[8:], 0)
	binary.BigEndian.PutUint32(pkt[12:], stamp)
	copy(pkt[32:], data)

	checkInput := append(pkt[:16], token...)
	checkInput = append(checkInput, data...)
	checksum := md5.Sum(checkInput)
	copy(pkt[16:32], checksum[:])
	return pkt
}

func md5bytes(data []byte) []byte {
	h := md5.Sum(data)
	return h[:]
}

// ─── 小米雲端 API ─────────────────────────────────────────────────────────────

func (x *XiaoAISpeaker) speakViaCloud(text string) error {
	if x.miToken == "" {
		if err := x.loginMiCloud(); err != nil {
			return fmt.Errorf("小米雲端登入失敗: %w", err)
		}
	}

	apiURL := fmt.Sprintf("https://%s.api.io.mi.com/app/aiservice/edge/request",
		x.getServerPrefix())

	payload, _ := json.Marshal(map[string]interface{}{
		"data": map[string]interface{}{
			"model":  x.cfg.XiaoAIDeviceID,
			"params": map[string]string{"text": text},
			"extra":  map[string]string{"execution": "immediate"},
		},
	})

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+x.miToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("雲端 API 請求失敗: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("雲端 API 錯誤 %d: %s", resp.StatusCode, respBody)
	}
	fmt.Printf("✅ [小愛同學] 雲端指令發送成功\n")
	return nil
}

func (x *XiaoAISpeaker) loginMiCloud() error {
	loginURL := "https://account.xiaomi.com/pass/serviceLoginAuth2"
	pwHash := fmt.Sprintf("%X", md5.Sum([]byte(x.cfg.XiaoAIPassword)))
	form := fmt.Sprintf("user=%s&_sign=&sid=xiaomiio&_json=true&hash=%s",
		x.cfg.XiaoAIMiID, pwHash)

	resp, err := http.Post(loginURL, "application/x-www-form-urlencoded",
		strings.NewReader(form))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	cleanBody := strings.TrimPrefix(string(body), "&&&START&&&")
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(cleanBody), &result); err != nil {
		return fmt.Errorf("解析登入回應失敗: %w", err)
	}
	if token, ok := result["token"].(string); ok {
		x.miToken = token
		return nil
	}
	return fmt.Errorf("登入失敗，請確認帳號密碼")
}

// ─── python-miio CLI ─────────────────────────────────────────────────────────

func (x *XiaoAISpeaker) speakViaMiIOCli(text string) error {
	deviceIP := x.getDeviceIP()
	deviceToken := os.Getenv("XIAOAI_DEVICE_TOKEN")
	if deviceIP == "" || deviceToken == "" {
		return fmt.Errorf("需要設定 XIAOAI_DEVICE_IP 和 XIAOAI_DEVICE_TOKEN")
	}
	cmd := exec.Command("miiocli", "xioamiio",
		"--ip", deviceIP, "--token", deviceToken, "say", text)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("miiocli 失敗: %w\n%s", err, out)
	}
	fmt.Printf("✅ [小愛同學] miiocli 成功: %s\n", out)
	return nil
}

// ─── 裝置探索 ─────────────────────────────────────────────────────────────────

func (x *XiaoAISpeaker) discoverMiIODevices() ([]Device, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	helloPacket := []byte{
		0x21, 0x31, 0x00, 0x20,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff,
	}

	broadcastAddr := &net.UDPAddr{IP: net.IPv4bcast, Port: 54321}
	conn.WriteTo(helloPacket, broadcastAddr)
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	var devices []Device
	buf := make([]byte, 1024)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if n >= 32 {
			deviceID := binary.BigEndian.Uint32(buf[8:12])
			devices = append(devices, Device{
				ID:       fmt.Sprintf("%08x", deviceID),
				Name:     fmt.Sprintf("小米裝置 %08x", deviceID),
				IP:       addr.IP.String(),
				Type:     "XiaoAI",
				IsOnline: true,
			})
		}
	}
	return devices, nil
}

// ─── 工具 ─────────────────────────────────────────────────────────────────────

func (x *XiaoAISpeaker) getDeviceIP() string {
	return os.Getenv("XIAOAI_DEVICE_IP")
}

func (x *XiaoAISpeaker) getServerPrefix() string {
	switch x.miServer {
	case "us":
		return "us"
	case "sg", "tw":
		return "sg"
	default:
		return "cn"
	}
}
