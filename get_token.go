//go:build ignore
// +build ignore

// 執行方式：go run get_token.go <帳號> <密碼> [區域]

package main

import (
	"bufio"
	"crypto/md5"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"
)

var serverURLs = map[string]string{
	"cn": "https://api.io.mi.com/app",
	"sg": "https://sg.api.io.mi.com/app",
	"us": "https://us.api.io.mi.com/app",
	"de": "https://de.api.io.mi.com/app",
	"tw": "https://sg.api.io.mi.com/app",
}

type miClient struct {
	http      *http.Client
	userID    string
	ssecurity string
	token     string
}

func newMiClient() *miClient {
	jar, _ := cookiejar.New(nil)
	return &miClient{
		http: &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			},
		},
	}
}

func (c *miClient) login(username, password string) error {
	// Step 1: 取得 _sign
	req1, _ := http.NewRequest("GET",
		"https://account.xiaomi.com/pass/serviceLogin?sid=xiaomiio&_json=true", nil)
	req1.Header.Set("User-Agent", "APP/com.xiaomi.mihome APPV/6.0.103 channel/MI-APP-STORE")

	resp1, err := c.http.Do(req1)
	if err != nil {
		return fmt.Errorf("Step1 失敗: %w", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	clean1 := strings.TrimPrefix(string(body1), "&&&START&&&")
	var step1 map[string]interface{}
	if err := json.Unmarshal([]byte(clean1), &step1); err != nil {
		return fmt.Errorf("Step1 解析失敗: %w", err)
	}
	sign, _ := step1["_sign"].(string)

	// Step 2: 送出帳號密碼
	pwHash := strings.ToUpper(fmt.Sprintf("%x", md5.Sum([]byte(password))))
	form := url.Values{
		"user":         {username},
		"hash":         {pwHash},
		"_sign":        {sign},
		"sid":          {"xiaomiio"},
		"_json":        {"true"},
		"serviceParam": {`{"checkSafePhone":false}`},
	}

	req2, _ := http.NewRequest("POST",
		"https://account.xiaomi.com/pass/serviceLoginAuth2",
		strings.NewReader(form.Encode()))
	req2.Header.Set("User-Agent", "APP/com.xiaomi.mihome APPV/6.0.103 channel/MI-APP-STORE")
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp2, err := c.http.Do(req2)
	if err != nil {
		return fmt.Errorf("Step2 失敗: %w", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	clean2 := strings.TrimPrefix(string(body2), "&&&START&&&")
	var step2 map[string]interface{}
	if err := json.Unmarshal([]byte(clean2), &step2); err != nil {
		return fmt.Errorf("Step2 解析失敗: %w", err)
	}

	code, _ := step2["code"].(float64)
	if code != 0 {
		desc, _ := step2["desc"].(string)
		switch int(code) {
		case 70016:
			return fmt.Errorf("帳號或密碼錯誤")
		case -2:
			return fmt.Errorf("帳號不存在")
		default:
			return fmt.Errorf("登入失敗 code=%.0f: %s", code, desc)
		}
	}

	if uid, ok := step2["userId"].(float64); ok {
		c.userID = fmt.Sprintf("%.0f", uid)
	} else {
		c.userID, _ = step2["userId"].(string)
	}
	c.ssecurity, _ = step2["ssecurity"].(string)
	c.token, _ = step2["serviceToken"].(string)

	// ── 處理二步驟驗證（securityStatus != 0，notificationUrl 非空）────────────
	notifURL, _ := step2["notificationUrl"].(string)
	if c.token == "" && notifURL != "" {
		fmt.Println()
		fmt.Println("⚠️  小米帳號需要身份驗證")
		fmt.Println()
		fmt.Println("請按照以下步驟操作：")
		fmt.Println()
		fmt.Println("  👉 用瀏覽器開啟下方網址，登入並點「確認授權」：")
		fmt.Println()
		fmt.Println("  " + notifURL)
		fmt.Println()
		fmt.Println("  完成授權後，回到這裡按 Enter 繼續")
		fmt.Println()
		fmt.Print("✋ 瀏覽器授權完成後，按 Enter 繼續... ")
		bufio.NewReader(os.Stdin).ReadString('\n')

		// 用授權後的 location redirect 取得 serviceToken
		fmt.Println("🔄 取得 token 中...")
		token, err := c.fetchTokenAfterAuth(notifURL)
		if err != nil {
			return fmt.Errorf("取得 token 失敗: %w\n\n  請確認已在瀏覽器完成授權，然後重新執行程式", err)
		}
		c.token = token
	}

	if c.token == "" {
		return fmt.Errorf("未能取得 serviceToken，請稍後再試")
	}
	return nil
}

// fetchTokenAfterAuth 使用者在瀏覽器完成授權後，重新走一次登入流程取得 serviceToken
// 小米國際版（Gmail 帳號）在瀏覽器授權後，cookie 會帶 serviceToken
func (c *miClient) fetchTokenAfterAuth(notifURL string) (string, error) {
	// 先訪問授權 URL，看 cookie jar 是否已有 token
	req, _ := http.NewRequest("GET", notifURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("訪問授權頁失敗: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// 方法 1：檢查回應 JSON 中是否有 serviceToken
	clean := strings.TrimPrefix(string(body), "&&&START&&&")
	var result map[string]interface{}
	if json.Unmarshal([]byte(clean), &result) == nil {
		if token, ok := result["serviceToken"].(string); ok && token != "" {
			return token, nil
		}
		// 有 location 表示要再 redirect 一次
		if location, ok := result["location"].(string); ok && location != "" {
			locResp, err := c.http.Get(location)
			if err == nil {
				locResp.Body.Close()
			}
		}
	}

	// 方法 2：從 cookie jar 取 serviceToken
	u, _ := url.Parse("https://account.xiaomi.com")
	for _, cookie := range c.http.Jar.Cookies(u) {
		if cookie.Name == "serviceToken" || cookie.Name == "yetAnotherServiceToken" {
			if cookie.Value != "" {
				fmt.Printf("   ✅ 從 cookie 取得 token\n")
				return cookie.Value, nil
			}
		}
	}

	// 方法 3：直接提示使用者輸入 serviceToken（從瀏覽器 cookie 複製）
	fmt.Println()
	fmt.Println("  ⚠️  自動取得 token 失敗，請手動複製：")
	fmt.Println()
	fmt.Println("  1. 在完成授權的瀏覽器中，開啟開發者工具（F12）")
	fmt.Println("  2. 切換到「應用程式」→「Cookie」→「account.xiaomi.com」")
	fmt.Println("  3. 找到「serviceToken」，複製它的值")
	fmt.Println()
	fmt.Print("  貼上 serviceToken：")

	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		token := strings.TrimSpace(scanner.Text())
		if token != "" {
			return token, nil
		}
	}

	return "", fmt.Errorf("未能取得 serviceToken")
}

func (c *miClient) getDevices(server string) ([]map[string]interface{}, error) {
	baseURL, ok := serverURLs[server]
	if !ok {
		baseURL = serverURLs["sg"]
	}

	// 嘗試多個 API 端點（不同版本的小米 API 路徑不同）
	endpoints := []struct {
		path string
		body map[string]interface{}
	}{
		{"/home/device_list", map[string]interface{}{"getVirtualModel": false, "getHuamiDevices": 0}},
		{"/home/device_list_page", map[string]interface{}{"getVirtualModel": false, "getHuamiDevices": 0, "limit": 200, "start": ""}},
		{"/v2/home/device_list", map[string]interface{}{"getVirtualModel": false, "getHuamiDevices": 0}},
	}

	for _, ep := range endpoints {
		bodyBytes, _ := json.Marshal(ep.body)
		apiURL := baseURL + ep.path

		req, _ := http.NewRequest("POST", apiURL, strings.NewReader(string(bodyBytes)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "APP/com.xiaomi.mihome APPV/6.0.103 channel/MI-APP-STORE")
		req.Header.Set("x-xiaomi-protocal-flag-cli", "PROTOCAL-HTTP2")
		req.Header.Set("MIOT-ENCRYPT-ALGORITHM", "ENCRYPT-RC4")
		req.AddCookie(&http.Cookie{Name: "userId", Value: c.userID})
		req.AddCookie(&http.Cookie{Name: "serviceToken", Value: c.token})
		req.AddCookie(&http.Cookie{Name: "yetAnotherServiceToken", Value: c.token})
		req.AddCookie(&http.Cookie{Name: "locale", Value: "zh_TW"})

		resp, err := c.http.Do(req)
		if err != nil {
			fmt.Printf("   [%s%s] 請求失敗: %v\n", server, ep.path, err)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// 印出原始回應（除錯用）
		preview := string(respBody)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		fmt.Printf("   [%s%s] 回應: %s\n", server, ep.path, preview)

		var result map[string]interface{}
		if err := json.Unmarshal(respBody, &result); err != nil {
			continue
		}

		// 檢查錯誤碼
		if code, ok := result["code"].(float64); ok && code != 0 {
			fmt.Printf("   [%s%s] API code=%.0f\n", server, ep.path, code)
			continue
		}

		// 嘗試不同的資料結構
		var list []interface{}
		if r, ok := result["result"].(map[string]interface{}); ok {
			list, _ = r["list"].([]interface{})
		} else if l, ok := result["result"].([]interface{}); ok {
			list = l
		}

		if len(list) > 0 {
			var devices []map[string]interface{}
			for _, item := range list {
				if d, ok := item.(map[string]interface{}); ok {
					devices = append(devices, d)
				}
			}
			return devices, nil
		}
	}

	return nil, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	username := os.Getenv("XIAOAI_MI_ID")
	password := os.Getenv("XIAOAI_PASSWORD")
	server := os.Getenv("XIAOAI_MI_SERVER")
	if server == "" {
		server = "cn"
	}
	if len(os.Args) >= 3 {
		username, password = os.Args[1], os.Args[2]
	}
	if len(os.Args) >= 4 {
		server = os.Args[3]
	}
	if username == "" || password == "" {
		fmt.Println("用法：go run get_token.go <帳號> <密碼> [區域]")
		fmt.Println("區域：cn（中國/預設）| sg（台灣）| us（美洲）| de（歐洲）")
		os.Exit(1)
	}

	fmt.Printf("🔑 登入中 [%s]（區域: %s）...\n", username, server)
	client := newMiClient()
	if err := client.login(username, password); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ 登入成功！userId: %s\n\n", client.userID)

	// 自動嘗試所有區域
	serversToTry := []string{server}
	for s := range serverURLs {
		if s != server && s != "tw" {
			serversToTry = append(serversToTry, s)
		}
	}

	var devices []map[string]interface{}
	var usedServer string
	for _, s := range serversToTry {
		fmt.Printf("📡 查詢裝置清單（%s）...\n", s)
		d, err := client.getDevices(s)
		if err != nil {
			fmt.Printf("   ⚠️  %s: %v\n", s, err)
			continue
		}
		if len(d) > 0 {
			devices, usedServer = d, s
			break
		}
		fmt.Printf("   （%s 沒有裝置）\n", s)
	}

	if len(devices) == 0 {
		fmt.Println("\n⚠️  所有區域均未找到裝置，請確認帳號已綁定裝置")
		os.Exit(0)
	}

	fmt.Printf("\n✅ 在 [%s] 找到 %d 個裝置：\n", usedServer, len(devices))
	fmt.Println(strings.Repeat("─", 65))

	var speakers []map[string]interface{}
	for _, d := range devices {
		name, _ := d["name"].(string)
		model, _ := d["model"].(string)
		did, _ := d["did"].(string)
		ip, _ := d["localip"].(string)
		token, _ := d["token"].(string)
		isOnline, _ := d["isOnline"].(bool)

		isSpeaker := strings.Contains(strings.ToLower(model), "speaker") ||
			strings.Contains(name, "音箱") || strings.Contains(name, "小愛")
		tag := ""
		if isSpeaker {
			tag = "  🤖"
			speakers = append(speakers, d)
		}

		status := "🔴 離線"
		if isOnline {
			status = "🟢 在線"
		}
		if ip == "" {
			ip = "（未知）"
		}

		tokenStr := "⚠️  未取得"
		if token != "" {
			if len(token) == 32 {
				tokenStr = token + "  ✅"
			} else {
				tokenStr = token
			}
		}

		fmt.Printf("\n%s%s\n  型號: %s\n  ID:   %s\n  IP:   %s  %s\n  Token:%s\n",
			name, tag, model, did, ip, status, tokenStr)
	}

	fmt.Println("\n" + strings.Repeat("─", 65))

	target := speakers
	if len(target) == 0 {
		target = devices
	}

	fmt.Println("\n💡 PowerShell 環境變數設定（複製貼上）：\n")
	for _, d := range target {
		name, _ := d["name"].(string)
		ip, _ := d["localip"].(string)
		token, _ := d["token"].(string)
		did, _ := d["did"].(string)
		if token == "" {
			continue
		}
		fmt.Printf("# %s\n", name)
		fmt.Printf("$env:XIAOAI_DEVICE_IP    = '%s'\n", ip)
		fmt.Printf("$env:XIAOAI_DEVICE_TOKEN = '%s'\n", token)
		fmt.Printf("$env:XIAOAI_DEVICE_ID    = '%s'\n", did)
		fmt.Printf("$env:XIAOAI_MI_SERVER    = '%s'\n\n", usedServer)
	}
}
