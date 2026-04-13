// Package core — движок Bip-Bop VPN.
// Архитектура 4.2-PURE: 100% идентичность olcrtc.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"math/rand"
)

const (
	Version     = "4.8-ULTRA-BOOST"
	DefPort     = "8443"
	MaxBackoff  = 60 * time.Second
	HealthEvery = 15 * time.Second
	CredsTTL    = 5 * time.Minute
)

// Logger is the interface both CLI and mobile provide for log output.
type Logger interface {
	Info(msg string)
	Warn(msg string)
	Error(msg string)
}

// StatusListener receives tunnel state changes.
type StatusListener interface {
	OnStatus(status string)
	OnTurnInfo(url string)
	OnStats(tx, rx int64)
}

// --- default no-op implementations ---

type nopLogger struct{}

func (nopLogger) Info(string)  {}
func (nopLogger) Warn(string)  {}
func (nopLogger) Error(string) {}

type nopStatus struct{}

func (nopStatus) OnStatus(string)      {}
func (nopStatus) OnTurnInfo(string)    {}
func (nopStatus) OnStats(int64, int64) {}

// --- globals set by the host ---

var (
	mu  sync.Mutex
	Log Logger         = nopLogger{}
	Lis StatusListener = nopStatus{}
)

func SetLogger(l Logger)           { mu.Lock(); Log = l; mu.Unlock() }
func SetListener(l StatusListener) { mu.Lock(); Lis = l; mu.Unlock() }

func getLog() Logger         { mu.Lock(); defer mu.Unlock(); return Log }
func getLis() StatusListener { mu.Lock(); defer mu.Unlock(); return Lis }

// --- Key derivation ---

func DeriveKey(pw string) []byte { h := sha256.Sum256([]byte(pw)); return h[:] }

// --- Smart-key ---

func ParseSmartKey(k string) (roomURL, pw string, err error) {
	var d []byte
	d, err = base64.RawURLEncoding.DecodeString(k)
	if err != nil {
		d, err = base64.RawStdEncoding.DecodeString(k)
		if err != nil {
			return "", "", fmt.Errorf("invalid smart-key")
		}
	}
	parts := strings.SplitN(string(d), "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("corrupted smart-key")
	}
	roomURL = parts[0]
	pw = parts[1]
	return
}

func EncodeSmartKey(roomURL, password string) string {
	raw := roomURL + "|" + password
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func SmartKeyServerIP(k string) (string, error) {
	room, _, err := ParseSmartKey(k)
	return room, err
}

// --- Establish tunnel ---

func Establish(cache *CredsCache, key, name string, isServer bool, onDisconnect func()) (*Multiplexer, io.Closer, error) {
	log := getLog()
	log.Info(fmt.Sprintf("[ENG] Establishing tunnel... (Version: %s)", Version))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	roomURL, _, err := ParseSmartKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid smart-key: %w", err)
	}
	log.Info(fmt.Sprintf("[ENG] Smart-key parsed. Room: %s", roomURL))

	peer, err := NewWebRTCPeer(roomURL, name, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to init WebRTC: %w", err)
	}
	log.Info("[ENG] WebRTC Peer initialized.")

	// Генерируем случайный ClientID для каждой новой сессии WebRTC,
	// чтобы сервер понимал, что это новый поток данных и сбросил seq.
	clientID := uint32(rand.New(rand.NewSource(time.Now().UnixNano())).Uint32())
	if clientID == 0 { clientID = 1 }

	mux := NewMultiplexer(clientID, func(data []byte) error {
		return peer.Send(data)
	})

	// Привязываем обработку данных из DataChannel к мультиплексору
	peer.onData = mux.HandleFrame
	peer.OnDisconnected = onDisconnect

	log.Info(fmt.Sprintf("[ENG] Connecting to Telemost Room... (%s)", roomURL))

	if err := peer.Connect(ctx); err != nil {
		peer.Close()
		log.Error(fmt.Sprintf("[ENG] Telemost connect failed: %v", err))
		return nil, nil, fmt.Errorf("telemost connect error: %w", err)
	}

	log.Info("[ENG] DataChannel established and Mux ready!")

	return mux, peer, nil
}

// --- Session wrapper ---

type Session struct {
	sync.RWMutex
	Mux     *Multiplexer
	Cl      io.Closer
	Ch      chan struct{}
	Ok      bool
	TxBytes atomic.Int64
	RxBytes atomic.Int64
}

func (s *Session) Set(m *Multiplexer, c io.Closer) {
	getLog().Info("[ENG] Session.Set() called")
	s.Lock()
	defer s.Unlock()
	if s.Cl != nil {
		s.Cl.Close()
	}
	if s.Ch != nil {
		select {
		case <-s.Ch:
		default:
			close(s.Ch)
		}
	}
	s.Ch = make(chan struct{})
	s.Mux, s.Cl, s.Ok = m, c, true
}

func (s *Session) Wait() <-chan struct{} {
	s.RLock()
	defer s.RUnlock()
	return s.Ch
}

func (s *Session) Get() (*Multiplexer, bool) {
	s.RLock()
	defer s.RUnlock()
	return s.Mux, s.Ok
}

func (s *Session) Down() {
	s.Lock()
	defer s.Unlock()
	s.Ok = false
	if s.Ch != nil {
		select {
		case <-s.Ch:
		default:
			close(s.Ch)
		}
	}
}

func (s *Session) Stop() {
	s.Lock()
	defer s.Unlock()
	if s.Cl != nil {
		s.Cl.Close()
		s.Cl = nil
	}
	if s.Ch != nil {
		select {
		case <-s.Ch:
		default:
			close(s.Ch)
		}
	}
	s.Mux, s.Ok = nil, false
}

// --- SOCKS5 handshake (helpers) ---

func SocksHandshake(c net.Conn) (string, error) {
	buf := make([]byte, 258)
	if _, err := io.ReadFull(c, buf[:2]); err != nil || buf[0] != 0x05 {
		return "", fmt.Errorf("socks5 only")
	}
	n := int(buf[1])
	if n > 0 {
		if _, err := io.ReadFull(c, buf[:n]); err != nil {
			return "", err
		}
	}
	c.Write([]byte{0x05, 0x00})

	if _, err := io.ReadFull(c, buf[:4]); err != nil || buf[0] != 0x05 || buf[1] != 0x01 {
		return "", fmt.Errorf("socks5 connect only")
	}

	addrType := buf[3]
	var host string
	switch addrType {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 0x03:
		b := make([]byte, 1)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		sz := int(b[0])
		db := make([]byte, sz)
		if _, err := io.ReadFull(c, db); err != nil {
			return "", err
		}
		host = string(db)
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	default:
		return "", fmt.Errorf("unsupported addr type %d", addrType)
	}

	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return "", err
	}
	port := int(pb[0])<<8 | int(pb[1])

	c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	return fmt.Sprintf("%s:%d", host, port), nil
}

// CredsCache - заглушка
type CredsCache struct{}
func NewCredsCache() *CredsCache { return &CredsCache{} }
