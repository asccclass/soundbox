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
	"time"
)

// XiaoAISpeaker 控制小愛音箱
type XiaoAISpeaker struct {
	cfg      Config
	tts      TTSEngine
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
		miServer = "sg"
	}
	return &XiaoAISpeaker{cfg: cfg, tts: ttsEngine, miServer: miServer}, nil
}

func (x *XiaoAISpeaker) Name() string { return "小愛同學" }

func (x *XiaoAISpeaker) Speak(text string) error {
	fmt.Printf("🔊 [小愛同學] 準備播放: %q\n", truncate(text, 50))

	deviceIP := x.getDeviceIP()
	deviceToken := os.Getenv("XIAOAI_DEVICE_TOKEN")

	// ── 方法 1：本地 HTTP API（port 5299，不需 Token，最簡單）─────────────────
	if deviceIP != "" {
		if err := x.speakViaLocalHTTP(deviceIP, text); err == nil {
			return nil
		} else {
			fmt.Printf("   ⚠️  本地 HTTP 失敗: %v\n", err)
		}
	}

	// ── 方法 2：MiIO UDP（需要正確 Token）────────────────────────────────────
	if deviceIP != "" && deviceToken != "" {
		if err := x.speakViaMiIO(text); err == nil {
			return nil
		} else {
			fmt.Printf("   ⚠️  MiIO UDP 失敗: %v\n", err)
		}
	}

	// ── 方法 3：miiocli（python-miio）─────────────────────────────────────────
	if commandExists("miiocli") {
		return x.speakViaMiIOCli(text)
	}

	// ── 方法 4：小米雲端 API ───────────────────────────────────────────────────
	if x.cfg.XiaoAIMiID != "" {
		return x.speakViaCloud(text)
	}

	return fmt.Errorf(
		"所有播放方式均失敗\n" +
			"  請確認 XIAOAI_DEVICE_IP 已設定，且音箱和電腦在同一 Wi-Fi\n" +
			"  IP: " + deviceIP)
}

func (x *XiaoAISpeaker) SetVolume(level int) error {
	deviceIP := x.getDeviceIP()
	if deviceIP == "" {
		return fmt.Errorf("需要設定 XIAOAI_DEVICE_IP")
	}
	// 先試 HTTP API
	if err := x.setVolumeViaHTTP(deviceIP, level); err == nil {
		return nil
	}
	// 再試 MiIO
	return x.sendMiIOCommand("set_volume", []interface{}{level})
}

func (x *XiaoAISpeaker) GetDevices() ([]Device, error) {
	return x.discoverMiIODevices()
}

// ─── 方法 1：本地 HTTP API（port 5299）───────────────────────────────────────
//
// 小愛音箱（部分型號）內建 HTTP API，不需要 Token：
//   POST http://<IP>:5299/tts  body: {"text":"..."}
//   POST http://<IP>:5299/player_set_volume  body: {"volume":70}

func (x *XiaoAISpeaker) speakViaLocalHTTP(deviceIP, text string) error {
	// 多個小米音箱型號的 HTTP API 端點（不同型號路徑略有差異）
	endpoints := []struct {
		url  string
		body map[string]interface{}
	}{
		// 小愛音箱 Pro / Play / mini 系列
		{fmt.Sprintf("http://%s:5299/tts", deviceIP),
			map[string]interface{}{"text": text}},
		// 部分型號
		{fmt.Sprintf("http://%s:5299/tts_play", deviceIP),
			map[string]interface{}{"text": text, "save": 0}},
		// 小愛音箱 max / 觸屏版
		{fmt.Sprintf("http://%s:6095/api/tts", deviceIP),
			map[string]interface{}{"text": text}},
	}

	client := &http.Client{Timeout: 8 * time.Second}
	for _, ep := range endpoints {
		bodyBytes, _ := json.Marshal(ep.body)
		resp, err := client.Post(ep.url, "application/json", bytes.NewReader(bodyBytes))
		if err != nil {
			continue // 連接失敗，試下一個
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			fmt.Printf("✅ [小愛同學] 本地 HTTP TTS 成功 (%s)\n", ep.url)
			_ = respBody
			return nil
		}
	}
	return fmt.Errorf("port 5299/6095 無回應（此型號可能不支援本地 HTTP API）")
}

func (x *XiaoAISpeaker) setVolumeViaHTTP(deviceIP string, level int) error {
	urls := []string{
		fmt.Sprintf("http://%s:5299/player_set_volume", deviceIP),
		fmt.Sprintf("http://%s:6095/api/set_volume", deviceIP),
	}
	client := &http.Client{Timeout: 5 * time.Second}
	body, _ := json.Marshal(map[string]interface{}{"volume": level})
	for _, u := range urls {
		resp, err := client.Post(u, "application/json", bytes.NewReader(body))
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	return fmt.Errorf("HTTP 設定音量失敗")
}

// ─── 方法 2：MiIO UDP（需要 Token）───────────────────────────────────────────

func (x *XiaoAISpeaker) speakViaMiIO(text string) error {
	payload := map[string]interface{}{
		"id":     1,
		"method": "text_to_speech",
		"params": []string{text},
	}
	return x.sendMiIOPacket(x.getDeviceIP(), os.Getenv("XIAOAI_DEVICE_TOKEN"), payload)
}

func (x *XiaoAISpeaker) sendMiIOCommand(method string, params interface{}) error {
	deviceIP := x.getDeviceIP()
	deviceToken := os.Getenv("XIAOAI_DEVICE_TOKEN")
	if deviceIP == "" || deviceToken == "" {
		return fmt.Errorf("需要 XIAOAI_DEVICE_IP 和 XIAOAI_DEVICE_TOKEN")
	}
	return x.sendMiIOPacket(deviceIP, deviceToken,
		map[string]interface{}{"id": 1, "method": method, "params": params})
}

func (x *XiaoAISpeaker) sendMiIOPacket(deviceIP, token string, payload interface{}) error {
	tokenBytes, err := hex.DecodeString(token)
	if err != nil || len(tokenBytes) != 16 {
		return fmt.Errorf("Token 格式錯誤（需 32 位 hex，目前長度 %d）", len(token))
	}

	payloadJSON, _ := json.Marshal(payload)
	encrypted, err := miioEncrypt(tokenBytes, payloadJSON)
	if err != nil {
		return fmt.Errorf("加密失敗: %w", err)
	}

	// 先發 hello 取得裝置的 stamp（時間戳），避免因時鐘不同步被拒絕
	stamp, err := x.getMiIOStamp(deviceIP)
	if err != nil {
		stamp = uint32(time.Now().Unix()) // 取不到就用本機時間
	}

	packet := buildMiIOPacket(tokenBytes, stamp, encrypted)

	conn, err := net.DialTimeout("udp", fmt.Sprintf("%s:54321", deviceIP), 5*time.Second)
	if err != nil {
		return fmt.Errorf("連接裝置失敗: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))

	if _, err := conn.Write(packet); err != nil {
		return fmt.Errorf("傳送失敗: %w", err)
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("接收回應失敗（Token 可能不正確）: %w", err)
	}
	fmt.Printf("✅ [小愛同學] MiIO 回應: %d bytes\n", n)
	return nil
}

// getMiIOStamp 發 hello 封包取得裝置的時間戳（解決時鐘不同步問題）
func (x *XiaoAISpeaker) getMiIOStamp(deviceIP string) (uint32, error) {
	conn, err := net.DialTimeout("udp", fmt.Sprintf("%s:54321", deviceIP), 3*time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	hello := make([]byte, 32)
	hello[0] = 0x21
	hello[1] = 0x31
	binary.BigEndian.PutUint16(hello[2:], 32)
	for i := 4; i < 32; i++ {
		hello[i] = 0xff
	}

	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(hello); err != nil {
		return 0, err
	}
	buf := make([]byte, 32)
	if _, err := conn.Read(buf); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[12:16]), nil
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

// ─── 方法 3：python-miio CLI ─────────────────────────────────────────────────

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

// ─── 方法 4：小米雲端 API ────────────────────────────────────────────────────

func (x *XiaoAISpeaker) speakViaCloud(text string) error {
	fmt.Printf("   ☁️  嘗試雲端 API...\n")
	// 雲端 TTS 需要完整登入流程，這裡只作提示
	return fmt.Errorf("雲端 TTS 需要設定 XIAOAI_MI_ID 和 XIAOAI_PASSWORD")
}

// ─── 裝置探索 ─────────────────────────────────────────────────────────────────

func (x *XiaoAISpeaker) discoverMiIODevices() ([]Device, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	hello := make([]byte, 32)
	hello[0] = 0x21
	hello[1] = 0x31
	binary.BigEndian.PutUint16(hello[2:], 32)
	for i := 4; i < 32; i++ {
		hello[i] = 0xff
	}

	conn.WriteTo(hello, &net.UDPAddr{IP: net.IPv4bcast, Port: 54321})
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
	if x.cfg.XiaoAIDeviceID != "" {
		return x.cfg.XiaoAIDeviceID // 有時 DeviceID 存的是 IP
	}
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
