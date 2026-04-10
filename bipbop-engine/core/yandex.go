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
	// Stable Yandex API IPs to try if DNS fails
	yandexAPIFallbacks = []string{
		"213.180.204.127",
		"87.250.250.244",
		"77.88.21.127",
	}

	// Custom resolver that bypasses system DNS
	antiJammerResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: time.Second * 5,
			}
			// Force Yandex DNS via UDP to bypass system DNS tampering
			return d.DialContext(ctx, "udp", "77.88.8.8:53")
		},
	}

	antiJammerClient = &http.Client{
		Timeout: time.Second * 15,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, _ := net.SplitHostPort(addr)
				if host == "cloud-api.yandex.ru" || host == "api.cloud.yandex.net" {
					// Try our custom resolver first
					ips, err := antiJammerResolver.LookupHost(ctx, host)
					if err == nil && len(ips) > 0 {
						addr = net.JoinHostPort(ips[0], port)
					} else {
						// DNS Failed or timed out (Jammer!) -> Try fallbacks
						getLog().Warn(fmt.Sprintf("[API] DNS Lookup failed for %s: %v. Using fallbacks.", host, err))
						addr = net.JoinHostPort(yandexAPIFallbacks[0], port)
					}
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

	resp, err := antiJammerClient.Do(req)
	if err != nil {
		// Second chance: try next fallback IP directly in the URL if the first one failed
		getLog().Error(fmt.Sprintf("[API] First attempt failed: %v. Trying hardcoded fallback IP...", err))
		
		furl := fmt.Sprintf("https://%s/telemost_front/v2/telemost/conferences/%s/connection", 
			yandexAPIFallbacks[1], url.QueryEscape(roomURL))
		req2, _ := http.NewRequest("GET", furl, nil)
		req2.URL.RawQuery = q.Encode()
		req2.Host = "cloud-api.yandex.ru" // Critical for SNI and routing
		
		// Copy headers
		for k, vv := range req.Header {
			for _, v := range vv {
				req2.Header.Add(k, v)
			}
		}

		resp, err = antiJammerClient.Do(req2)
		if err != nil {
			return nil, err
		}
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
		if iceRaw, ok := cc["ice_servers"]; ok {
			iceJSON, _ := json.Marshal(iceRaw)
			getLog().Info(fmt.Sprintf("[API] ICE servers from Yandex: %s", string(iceJSON)))
		} else {
			getLog().Warn("[API] No ice_servers in client_configuration!")
			// Log all keys in client_configuration
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
