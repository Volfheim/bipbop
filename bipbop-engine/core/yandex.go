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
)

var (
	// Stable Yandex API IP for fallback (as a last resort bypass)
	yandexStableIP = "213.180.204.127"

	antiJammerClient = &http.Client{
		Timeout: time.Second * 15,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, _ := net.SplitHostPort(addr)
				if host == "cloud-api.yandex.ru" {
					dialer := net.Dialer{Timeout: 5 * time.Second}
					conn, err := dialer.DialContext(ctx, network, addr)
					if err == nil {
						return conn, nil
					}
					// DNS / Direct SNI fallback -> Try hardcoded Yandex IP
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

type CredsCache struct{}

// GetConnectionInfo запрашивает параметры подключения напрямую у Яндекса.
// Логика полностью соответствует olcrtc: прямая маскировка под браузер.
func GetConnectionInfo(roomURL, displayName string, _ ...string) (*ConnectionInfo, error) {
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

	// Headers copied from olcrtc (Mozilla/5.0 trick + telemost client version)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:149.0) Gecko/20100101 Firefox/149.0")
	req.Header.Set("X-Telemost-Client-Version", "187.1.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Client-Instance-Id", uuid.New().String())
	req.Header.Set("Idempotency-Key", uuid.New().String())
	req.Header.Set("Origin", "https://telemost.yandex.ru")
	req.Header.Set("Referer", "https://telemost.yandex.ru/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")

	resp, err := antiJammerClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("direct Yandex call failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Yandex API error %d: %s", resp.StatusCode, bodyBytes)
	}

	var info ConnectionInfo
	if err := json.Unmarshal(bodyBytes, &info); err != nil {
		return nil, err
	}

	return &info, nil
}
