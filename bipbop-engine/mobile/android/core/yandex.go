package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/proxy"
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

// GetConnectionInfo запрашивает инфу для входа в телемост
func GetConnectionInfo(roomURL, displayName string) (*ConnectionInfo, error) {
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

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Client-Instance-Id", uuid.New().String())
	req.Header.Set("X-Telemost-Client-Version", "190.1.0")
	req.Header.Set("Idempotency-Key", uuid.New().String())
	req.Header.Set("Origin", "https://telemost.yandex.ru")
	req.Header.Set("Referer", "https://telemost.yandex.ru/")

	getLog().Info("[API] Resolving cloud-api.yandex.ru...")

	// Build HTTP client
	var client *http.Client

	// Custom dialer that protects sockets on Android
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				if SocketProtector != nil {
					SocketProtector(int(fd))
				}
			})
		},
	}

	upstream := GetUpstream()
	if upstream != "" {
		getLog().Info(fmt.Sprintf("[API] Using upstream proxy: %s", upstream))
		proxyDialer, proxyErr := proxy.SOCKS5("tcp", upstream, nil, dialer)
		if proxyErr == nil {
			client = &http.Client{
				Transport: &http.Transport{Dial: proxyDialer.Dial},
				Timeout:   15 * time.Second,
			}
		} else {
			getLog().Warn(fmt.Sprintf("[API] Upstream proxy error: %v, falling back to direct", proxyErr))
			client = &http.Client{
				Transport: &http.Transport{Dial: dialer.Dial},
				Timeout:   15 * time.Second,
			}
		}
	} else {
		// On Android we MUST use a custom transport if we want to call VpnService.protect().
		// However, we must be careful with DNS. Use the protected dialer.
		client = &http.Client{
			Transport: &http.Transport{DialContext: dialer.DialContext},
			Timeout:   15 * time.Second,
		}
	}

	getLog().Info("[API] Sending request to Yandex Telemost API...")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	getLog().Info(fmt.Sprintf("[API] Response status: %d", resp.StatusCode))

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
		if iceRaw, ok := cc["ice_servers"]; ok {
			iceJSON, _ := json.Marshal(iceRaw)
			getLog().Info(fmt.Sprintf("[API] ICE servers from Yandex: %s", string(iceJSON)))
		} else {
			getLog().Warn("[API] No ice_servers in client_configuration!")
			keys := make([]string, 0)
			for k := range cc {
				keys = append(keys, k)
			}
			getLog().Info(fmt.Sprintf("[API] client_configuration keys: %v", keys))
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
