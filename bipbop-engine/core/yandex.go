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

const apiBase = "https://cloud-api.yandex.ru/telemost_front/v2/telemost"

// Пул IP-адресов для cloud-api.yandex.ru для обхода блокировок DNS (lookup timeout)
var apiIPs = []string{
	"213.180.204.127",
	"213.180.193.127",
	"77.88.21.127",
	"87.250.250.127",
	"5.255.255.127",
}

type ConnectionInfo struct {
	RoomID       string `json:"room_id"`
	PeerID       string `json:"peer_id"`
	Credentials  string `json:"credentials"`
	ClientConfig struct {
		MediaServerURL string `json:"media_server_url"`
	} `json:"client_configuration"`
}

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

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:149.0) Gecko/20100101 Firefox/149.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Client-Instance-Id", uuid.New().String())
	req.Header.Set("X-Telemost-Client-Version", "187.1.0")
	req.Header.Set("Idempotency-Key", uuid.New().String())
	req.Header.Set("Origin", "https://telemost.yandex.ru")
	req.Header.Set("Referer", "https://telemost.yandex.ru/")

	// Прокачанный FragDialer для гипер-режима
	fd := &FragDialer{
		Dialer: net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, _ := net.SplitHostPort(addr)

				// 1. Пытаемся стандартно + Фрагментация
				conn, err := fd.DialContext(ctx, "tcp4", addr)
				if err == nil {
					return conn, nil
				}

				// 2. Если DNS подвел или таймаут, пробуем наш пул IP + Фрагментация
				if host == "cloud-api.yandex.ru" {
					getLog().Warn(fmt.Sprintf("[API] DNS failed for %s, trying fallback IPs + Fragmentation...", host))
					for _, ip := range apiIPs {
						conn, err = fd.DialContext(ctx, "tcp4", net.JoinHostPort(ip, port))
						if err == nil {
							getLog().Info(fmt.Sprintf("[API] Connected via fallback IP + Frag: %s", ip))
							return conn, nil
						}
					}
				}
				return nil, err
			},
			ForceAttemptHTTP2: false,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Yandex API unreachable (Hyper Mode Failed): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, body)
	}

	var info ConnectionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	return &info, nil
}

// CredsCache - заглушка для обратной совместимости
type CredsCache struct{}

func NewCredsCache() *CredsCache { return &CredsCache{} }
