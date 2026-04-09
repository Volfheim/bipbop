package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/volfheim/bipbop/core"
)

const clientsFile = "clients.json"

type Client struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RoomURL   string `json:"room_url"`
	Password  string `json:"password"`
	SmartKey  string `json:"smart_key"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at"`
	RevokedAt string `json:"revoked_at,omitempty"`
}

type ClientStore struct {
	mu      sync.RWMutex
	Clients []Client `json:"clients"`
	path    string
}

func NewClientStore(dir string) *ClientStore {
	return &ClientStore{
		path: filepath.Join(dir, clientsFile),
	}
}

func (s *ClientStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.Clients = []Client{}
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &s.Clients)
}

func (s *ClientStore) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := json.MarshalIndent(s.Clients, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func (s *ClientStore) Add(name, roomURL string) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate unique ID and password
	idBytes := make([]byte, 4)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	pwBytes := make([]byte, 16)
	rand.Read(pwBytes)
	pw := hex.EncodeToString(pwBytes)

	smartKey := core.EncodeSmartKey(roomURL, pw)

	c := Client{
		ID:        id,
		Name:      name,
		RoomURL:   roomURL,
		Password:  pw,
		SmartKey:  smartKey,
		Active:    true,
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	s.Clients = append(s.Clients, c)
	return &c, nil
}

func (s *ClientStore) Revoke(id string) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.Clients {
		if s.Clients[i].ID == id {
			s.Clients[i].Active = false
			s.Clients[i].RevokedAt = time.Now().Format(time.RFC3339)
			return &s.Clients[i], nil
		}
	}
	return nil, fmt.Errorf("client %s not found", id)
}

func (s *ClientStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.Clients {
		if s.Clients[i].ID == id {
			s.Clients = append(s.Clients[:i], s.Clients[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("client %s not found", id)
}

func (s *ClientStore) GetActive() []Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var active []Client
	for _, c := range s.Clients {
		if c.Active {
			active = append(active, c)
		}
	}
	return active
}

func (s *ClientStore) IsPasswordActive(pw string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, c := range s.Clients {
		if c.Password == pw && c.Active {
			return true
		}
	}
	return false
}

func (s *ClientStore) FindByPassword(pw string) *Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i, c := range s.Clients {
		if c.Password == pw {
			return &s.Clients[i]
		}
	}
	return nil
}

func (s *ClientStore) GetAll() []Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Client, len(s.Clients))
	copy(out, s.Clients)
	return out
}
