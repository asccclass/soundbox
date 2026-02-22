package main

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// ─── 純 Go Chromecast 實作（TLS TCP:8009，不依賴 catt 或 proto 套件）─────────
//
// 播放流程：
//   1. TLS 連線到 <IP>:8009
//   2. CONNECT  → receiver-0
//   3. LAUNCH   → Default Media Receiver (appId: CC1AD845)
//   4. 等待 RECEIVER_STATUS → 拿 transportId / sessionId
//   5. CONNECT  → transportId
//   6. LOAD     → 傳入 audioURL + contentType
//   7. 等待 playerState=PLAYING，確認播放真正開始
//   8. 保持連線直到播放完畢（keepAlive 回應 PING）

type castConn struct {
	conn    net.Conn
	counter int
}

func newCastConn(deviceIP string) (*castConn, error) {
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 10 * time.Second},
		"tcp",
		deviceIP+":8009",
		&tls.Config{InsecureSkipVerify: true},
	)
	if err != nil {
		return nil, fmt.Errorf("無法連接 %s:8009 — 請確認裝置 IP 正確且在同一網路: %w", deviceIP, err)
	}
	return &castConn{conn: conn}, nil
}

func (c *castConn) Close() { c.conn.Close() }

func (c *castConn) nextID() int { c.counter++; return c.counter }

func (c *castConn) send(ns, src, dst, payload string) error {
	msg := protoMsg(src, dst, ns, payload)
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(msg)))
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.conn.Write(append(header, msg...))
	return err
}

func (c *castConn) recv(timeout time.Duration) (string, string, error) {
	c.conn.SetReadDeadline(time.Now().Add(timeout))
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return "", "", err
	}
	size := binary.BigEndian.Uint32(hdr)
	if size == 0 || size > 128*1024 {
		return "", "", fmt.Errorf("異常封包大小: %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(c.conn, buf); err != nil {
		return "", "", err
	}
	ns, pl := protoExtract(buf)
	return pl, ns, nil
}

// ─── ChromecastPlay：連接並播放，保持連線直到播放完畢 ─────────────────────────

// ChromecastPlay 對 Google Home 發送 LOAD，等待 PLAYING 狀態後
// 保持連線（keepAlive）直到 audioTTL 時間到，確保 HTTP 伺服器不會提早關閉。
func ChromecastPlay(deviceIP, audioURL, contentType string, audioTTL time.Duration) error {
	const (
		nsConn     = "urn:x-cast:com.google.cast.tp.connection"
		nsHB       = "urn:x-cast:com.google.cast.tp.heartbeat"
		nsReceiver = "urn:x-cast:com.google.cast.receiver"
		nsMedia    = "urn:x-cast:com.google.cast.media"
		src        = "sender-0"
		dst        = "receiver-0"
	)

	cc, err := newCastConn(deviceIP)
	if err != nil {
		return err
	}
	defer cc.Close()

	// 1. CONNECT
	if err := cc.send(nsConn, src, dst, `{"type":"CONNECT","origin":{}}`); err != nil {
		return fmt.Errorf("CONNECT 失敗: %w", err)
	}

	// 2. LAUNCH Default Media Receiver
	launch, _ := json.Marshal(map[string]interface{}{
		"type": "LAUNCH", "appId": "CC1AD845", "requestId": cc.nextID(),
	})
	if err := cc.send(nsReceiver, src, dst, string(launch)); err != nil {
		return fmt.Errorf("LAUNCH 失敗: %w", err)
	}

	// 3. 等待 RECEIVER_STATUS，取 transportId + sessionId
	fmt.Println("   ⏳ 等待 Default Media Receiver 啟動...")
	transportID, sessionID, err := waitTransport(cc, nsReceiver, nsHB, src, dst)
	if err != nil {
		return err
	}
	fmt.Printf("   🔗 session: %s\n", sessionID)

	// 4. CONNECT 到 media transport
	if err := cc.send(nsConn, src, transportID, `{"type":"CONNECT","origin":{}}`); err != nil {
		return fmt.Errorf("媒體 CONNECT 失敗: %w", err)
	}

	// 5. LOAD
	load, _ := json.Marshal(map[string]interface{}{
		"type":      "LOAD",
		"requestId": cc.nextID(),
		"sessionId": sessionID,
		"autoplay":  true,
		"media": map[string]interface{}{
			"contentId":   audioURL,
			"contentType": contentType,
			"streamType":  "BUFFERED",
		},
	})
	if err := cc.send(nsMedia, src, transportID, string(load)); err != nil {
		return fmt.Errorf("LOAD 失敗: %w", err)
	}

	// 6. 等待 playerState = PLAYING（確認音箱真正開始播放）
	if err := waitPlaying(cc, nsMedia, nsHB, src, transportID); err != nil {
		return err
	}

	// 7. 保持連線（回應 PING），直到音訊播完
	fmt.Printf("   🎵 播放中，等待 %v ...\n", audioTTL.Round(time.Second))
	keepAlive(cc, nsHB, src, dst, audioTTL)

	fmt.Println("✅ [Google Home] 播放完成")
	return nil
}

// waitTransport 等待 Media Receiver 啟動，回傳 transportId + sessionId
func waitTransport(cc *castConn, nsRcv, nsHB, src, dst string) (string, string, error) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		pl, ns, err := cc.recv(5 * time.Second)
		if err != nil {
			return "", "", fmt.Errorf("等待 receiverStatus 失敗: %w", err)
		}
		if ns == nsHB && containsStr(pl, "PING") {
			cc.send(nsHB, src, dst, `{"type":"PONG"}`)
			continue
		}
		if ns != nsRcv {
			continue
		}
		var msg map[string]interface{}
		if json.Unmarshal([]byte(pl), &msg) != nil {
			continue
		}
		if msg["type"] != "RECEIVER_STATUS" {
			continue
		}
		status, _ := msg["status"].(map[string]interface{})
		apps, _ := status["applications"].([]interface{})
		for _, a := range apps {
			app, _ := a.(map[string]interface{})
			tid, _ := app["transportId"].(string)
			sid, _ := app["sessionId"].(string)
			if tid != "" {
				return tid, sid, nil
			}
		}
	}
	return "", "", fmt.Errorf("逾時：Media Receiver 未啟動（30s）")
}

// waitPlaying 等待 MEDIA_STATUS 中 playerState = PLAYING
func waitPlaying(cc *castConn, nsMedia, nsHB, src, dst string) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		pl, ns, err := cc.recv(5 * time.Second)
		if err != nil {
			// 讀取逾時不算致命，繼續等
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return fmt.Errorf("等待播放狀態失敗: %w", err)
		}

		if ns == nsHB && containsStr(pl, "PING") {
			cc.send(nsHB, src, dst, `{"type":"PONG"}`)
			continue
		}
		if ns != nsMedia {
			continue
		}

		var msg map[string]interface{}
		if json.Unmarshal([]byte(pl), &msg) != nil {
			continue
		}

		switch msg["type"] {
		case "LOAD_FAILED":
			// 印出詳細原因幫助診斷
			reason, _ := msg["detailedErrorCode"].(float64)
			return fmt.Errorf(
				"Google Home 無法載入音訊（LOAD_FAILED, code=%.0f）\n"+
					"   可能原因：\n"+
					"   1. 音箱無法連回電腦（防火牆擋住 port 18765 / 19876）\n"+
					"   2. WAV 格式不相容（建議改用 --tts google 或 --tts azure 產生 MP3）\n"+
					"   請先在音箱所在的裝置瀏覽器開啟以下 URL 確認可存取：\n"+
					"   %s",
				reason, pl)
		case "LOAD_CANCELLED":
			return fmt.Errorf("Google Home 取消播放")
		case "MEDIA_STATUS":
			statuses, _ := msg["status"].([]interface{})
			for _, s := range statuses {
				st, _ := s.(map[string]interface{})
				playerState, _ := st["playerState"].(string)
				fmt.Printf("   📺 playerState: %s\n", playerState)
				switch playerState {
				case "PLAYING", "BUFFERING":
					return nil // 開始播放或緩衝中，都算成功
				case "IDLE":
					// IDLE 可能是 LOAD_FAILED 的另一種表現
					idleReason, _ := st["idleReason"].(string)
					if idleReason == "ERROR" {
						return fmt.Errorf(
							"Google Home 播放失敗（IDLE/ERROR）\n"+
								"   最常見原因：音箱無法從 http://電腦IP:18765 下載音訊\n"+
								"   請確認 Windows 防火牆允許 port 18765 的輸入連線：\n"+
								"   netsh advfirewall firewall add rule name=\"VoiceBriefing\" "+
								"protocol=TCP dir=in localport=18765 action=allow")
					}
				}
			}
		}
	}
	return fmt.Errorf("逾時：未收到 PLAYING 狀態（15s）")
}

// keepAlive 回應 PING 保持連線，直到 duration 到期
func keepAlive(cc *castConn, nsHB, src, dst string, duration time.Duration) {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		pl, ns, err := cc.recv(3 * time.Second)
		if err != nil {
			// 逾時或中斷都直接結束等待
			return
		}
		if ns == nsHB && containsStr(pl, "PING") {
			cc.send(nsHB, src, dst, `{"type":"PONG"}`)
		}
	}
}

// ─── 最小 protobuf 編解碼 ─────────────────────────────────────────────────────

func protoMsg(src, dst, ns, payload string) []byte {
	var b []byte
	b = protoAppendVarint(b, 1, 0)
	b = protoAppendStr(b, 2, src)
	b = protoAppendStr(b, 3, dst)
	b = protoAppendStr(b, 4, ns)
	b = protoAppendVarint(b, 5, 0)
	b = protoAppendStr(b, 6, payload)
	return b
}

func protoAppendVarint(buf []byte, field int, val uint64) []byte {
	tag := (uint64(field) << 3) | 0
	return append(buf, append(encVarint(tag), encVarint(val)...)...)
}

func protoAppendStr(buf []byte, field int, s string) []byte {
	tag := (uint64(field) << 3) | 2
	b := encVarint(tag)
	b = append(b, encVarint(uint64(len(s)))...)
	b = append(b, s...)
	return append(buf, b...)
}

func encVarint(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func protoExtract(data []byte) (ns, payload string) {
	i := 0
	for i < len(data) {
		tag, n := decVarint(data[i:])
		i += n
		field := int(tag >> 3)
		wire := tag & 0x7
		switch wire {
		case 0:
			_, n := decVarint(data[i:])
			i += n
		case 2:
			l, n := decVarint(data[i:])
			i += n
			end := i + int(l)
			if end > len(data) {
				return
			}
			s := string(data[i:end])
			if field == 4 {
				ns = s
			} else if field == 6 {
				payload = s
			}
			i = end
		default:
			return
		}
	}
	return
}

func decVarint(data []byte) (uint64, int) {
	var v uint64
	for i, b := range data {
		v |= uint64(b&0x7f) << (7 * uint(i))
		if b < 0x80 {
			return v, i + 1
		}
		if i >= 9 {
			break
		}
	}
	return 0, 1
}
