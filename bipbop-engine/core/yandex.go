package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"sync"
)

var (
	// Stable Yandex API IP for fallback
	yandexStableIP = "213.180.204.127"

	antiJammerClient = &http.Client{
		Timeout: time.Second * 15,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, _ := net.SplitHostPort(addr)
				if host == "cloud-api.yandex.ru" || host == "api.disk.yandex.net" {
					// 1. Try normal dial first (5s timeout for DNS)
					dialer := net.Dialer{Timeout: 5 * time.Second}
					conn, err := dialer.DialContext(ctx, network, addr)
					if err == nil {
						return conn, nil
					}
					
					// 2. DNS Failed or Blocked -> Try harcoded IP
					getLog().Warn(fmt.Sprintf("[API] DNS Lookup failed for %s. Using hardcoded IP fallback: %s", host, yandexStableIP))
					return dialer.DialContext(ctx, network, net.JoinHostPort(yandexStableIP, port))
				}
				d := net.Dialer{Timeout: 10 * time.Second}
				return d.DialContext(ctx, network, addr)
			},
		},
	}
)


const apiBase = "https://cloud-api.yandex.ru/telemost_front/v2/telemost"

type ICEServerInfo struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type ConnectionInfo struct {
	RoomID       string `json:"room_id"`
	PeerID       string `json:"peer_id"`
	Credentials  string `json:"credentials"`
	ClientConfig struct {
		MediaServerURL string          `json:"media_server_url"`
		ICEServers     []ICEServerInfo `json:"ice_servers"`
	} `json:"client_configuration"`
}

// CredsCache используется мобильным движком для кеширования учетных данных.
type CredsCache struct {
	sync.Mutex
	// Можно добавить поля для реального кеширования, если потребуется.
}

// GetConnectionInfo запрашивает инфу для входа в телемост
func GetConnectionInfo(roomURL, displayName string, proxyIP ...string) (*ConnectionInfo, error) {
	// 1. Пытаемся напрямую (с нашим Fallback IP)
	info, err := getRemoteConfig(roomURL, displayName)
	if err == nil {
		return info, nil
	}

	// 2. Если не вышло и у нас есть IP прокси (VPS)
	if len(proxyIP) > 0 && proxyIP[0] != "" {
		getLog().Warn(fmt.Sprintf("[API] Direct Yandex call failed. Trying via VPS Proxy: %s", proxyIP[0]))
		return getProxyConfig(proxyIP[0], roomURL, displayName)
	}

	return nil, err
}

func getRemoteConfig(roomURL, displayName string) (*ConnectionInfo, error) {
	u := fmt.Sprintf("%s/conferences/%s/connection", apiBase, url.QueryEscape(roomURL))

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Add("next_gen_media_platform_allowed", "true")
	q.Add("display_name", displayName)
	q.Add("waiting_room_supported", "true")
	req.URL.RawQuery = q.Encode()

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:149.0) Gecko/20100101 Firefox/149.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Client-Instance-Id", uuid.New().String())
	req.Header.Set("X-Telemost-Client-Version", "187.1.0")
	req.Header.Set("Idempotency-Key", uuid.New().String())
	req.Header.Set("Origin", "https://telemost.yandex.ru")
	req.Header.Set("Referer", "https://telemost.yandex.ru/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")

	resp, err := antiJammerClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Yandex API error %d: %s", resp.StatusCode, body)
	}

	// Read the full body for both debugging and parsing
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Log raw response for debugging
	var rawJSON map[string]interface{}
	json.Unmarshal(bodyBytes, &rawJSON)
	if cc, ok := rawJSON["client_configuration"].(map[string]interface{}); ok {
		iceRaw, iceOk := cc["ice_servers"]
		if iceOk {
			iceJSON, _ := json.Marshal(iceRaw)
			getLog().Info(fmt.Sprintf("[API] ICE servers from Yandex: %s", string(iceJSON)))
		} else {
			getLog().Warn("[API] No ice_servers in client_configuration!")
		}

		// Log other interesting fields for debugging
		if ms, ok := cc["media_server_url"].(string); ok {
			getLog().Info(fmt.Sprintf("[API] Raw MediaServerURL: %s", ms))
		}
	}

	var info ConnectionInfo
	if err := json.Unmarshal(bodyBytes, &info); err != nil {
		return nil, err
	}

	getLog().Info(fmt.Sprintf("[API] MediaServerURL: %s", info.ClientConfig.MediaServerURL))
	getLog().Info(fmt.Sprintf("[API] ICE servers count: %d", len(info.ClientConfig.ICEServers)))
	for i, s := range info.ClientConfig.ICEServers {
		getLog().Info(fmt.Sprintf("[API]   ICE[%d]: urls=%v user=%s", i, s.URLs, s.Username))
	}

	return &info, nil
}

func getProxyConfig(proxyIP, roomURL, displayName string) (*ConnectionInfo, error) {
	u := fmt.Sprintf("http://%s:80/config?url=%s", proxyIP, url.QueryEscape(roomURL))
	resp, err := http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("vps proxy error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vps proxy returned %d", resp.StatusCode)
	}

	var info ConnectionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}
