package core

import (
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

	// Используем FragDialer для обхода SNI-блокировок
	fd := &FragDialer{
		Dialer: net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: fd.DialContext,
			// DoH-like поведение: если обычный резолв упадет, Go попробует Dial заново.
			// В FragDialer мы можем добавить DoH логику позже, если обычный Dial будет виснуть на lookup.
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		// Если Client.Timeout exceeded, значит даже фрагментация на этом IP не помогла,
		// либо DNS всё еще лежит. 
		return nil, fmt.Errorf("Yandex API unreachable (DPI/DNS block): %w", err)
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
