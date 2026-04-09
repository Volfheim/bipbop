// Package core is the shared Lionheart tunnel engine.
// Both the CLI (cmd/lionheart) and mobile bridge (mobile/golib) import this.
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

	"github.com/hashicorp/yamux"
	"github.com/pion/webrtc/v4"
)

const (
	Version     = "1.2"
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
	mu            sync.Mutex
	Log Logger         = nopLogger{}
	Lis StatusListener = nopStatus{}
	UpstreamProxy string // SOCKS5 proxy for outbound connections (e.g. "127.0.0.1:10808")
)

func SetLogger(l Logger)           { mu.Lock(); Log = l; mu.Unlock() }
func SetListener(l StatusListener) { mu.Lock(); Lis = l; mu.Unlock() }
func SetUpstream(addr string)      { mu.Lock(); UpstreamProxy = addr; mu.Unlock() }
func GetUpstream() string          { mu.Lock(); defer mu.Unlock(); return UpstreamProxy }

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

// Функции-заглушки для обратной совместимости вызовов извне (если были)
func SmartKeyServerIP(k string) (string, error) {
	room, _, err := ParseSmartKey(k)
	return room, err
}

// --- Yamux config ---

func YmxCfg() *yamux.Config {
	c := yamux.DefaultConfig()
	c.EnableKeepAlive = true
	c.KeepAliveInterval = 10 * time.Second
	c.ConnectionWriteTimeout = 20 * time.Second
	c.StreamOpenTimeout = 20 * time.Second
	return c
}

// --- Closer helpers ---

type CloserFunc func()

func (f CloserFunc) Close() error { f(); return nil }

type MultiCloser struct{ CC []io.Closer }

func (m *MultiCloser) Close() error {
	for _, c := range m.CC {
		c.Close()
	}
	return nil
}

// Establish tunnel (Telemost DataChannel)
type AuthValidator func(string) bool

// Establish tunnel (Telemost DataChannel) - Client side helper
func Establish(roomURL, pw string, isServer bool, authValidator AuthValidator) (*yamux.Session, io.Closer, error) {
	// ... (предыдущий код Establish остается таким же, он используется клиентом)
	// Я обновлю его тело чуть позже, чтобы избежать дублирования логики с Listen, 
	// но пока просто добавлю Listen ниже.
	return establishInternal(roomURL, pw, isServer, authValidator)
}

func establishInternal(roomURL, pw string, isServer bool, authValidator AuthValidator) (*yamux.Session, io.Closer, error) {
	name := "Guest"
	if isServer { name = "Host" }

	peer, err := NewWebRTCPeer(roomURL, name, nil)
	if err != nil { return nil, nil, err }

	stream := NewDCStream(peer)
	if err := peer.Connect(context.Background()); err != nil {
		stream.Close()
		return nil, nil, err
	}

	var ym *yamux.Session
	if isServer {
		ym, err = yamux.Server(stream, YmxCfg())
	} else {
		ym, err = yamux.Client(stream, YmxCfg())
	}
	if err != nil {
		stream.Close()
		return nil, nil, err
	}

	if _, err := performAuth(ym, stream, pw, isServer, authValidator); err != nil {
		return nil, nil, err
	}

	return ym, &MultiCloser{[]io.Closer{ym, stream}}, nil
}

func performAuth(ym *yamux.Session, raw io.Closer, pw string, isServer bool, validator AuthValidator) (string, error) {
	log := getLog()
	if !isServer {
		s, err := ym.OpenStream()
		if err != nil { return "", err }
		defer s.Close()
		s.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprintf(s, "%s\n", pw)
		resp := make([]byte, 1)
		if _, err := s.Read(resp); err != nil { return "", err }
		if resp[0] != 0 { return "", fmt.Errorf("auth failed") }
		log.Info("[ENG] Auth successful")
		return pw, nil
	} else {
		s, err := ym.AcceptStream()
		if err != nil { return "", err }
		defer s.Close()
		s.SetDeadline(time.Now().Add(10 * time.Second))
		var rpw string
		if _, err := fmt.Fscanf(s, "%s\n", &rpw); err != nil {
			s.Write([]byte{1})
			return "", err
		}
		valid := false
		if validator != nil { valid = validator(rpw) } else { valid = (rpw == pw) }
		if !valid {
			s.Write([]byte{1})
			return "", fmt.Errorf("auth failed")
		}
		s.Write([]byte{0})
		log.Info("[ENG] Client authenticated")
		return rpw, nil
	}
}

// Listen starts a server in the specified room and handles multiple sessions
func Listen(roomURL, pw string, validator AuthValidator, handler func(string, *yamux.Session)) error {
	ctx := context.Background()
	peer, err := NewWebRTCPeer(roomURL, "Host", nil)
	if err != nil { return err }

	peer.onNewDC = func(dc *webrtc.DataChannel) {
		go func() {
			getLog().Info("[ENG] New client joining...")
			dcc := NewDCStreamFromDC(peer, dc)
			ym, err := yamux.Server(dcc, YmxCfg())
			if err != nil {
				dcc.Close()
				return
			}
			usedPw, err := performAuth(ym, dcc, pw, true, validator)
			if err != nil {
				ym.Close()
				dcc.Close()
				return
			}
			handler(usedPw, ym)
		}()
	}

	return peer.Connect(ctx)
}

// --- Session wrapper ---

type Session struct {
	sync.RWMutex
	Ym      *yamux.Session
	Cl      io.Closer
	Ch      chan struct{}
	Ok      bool
	TxBytes atomic.Int64
	RxBytes atomic.Int64
}

func (s *Session) Set(y *yamux.Session, c io.Closer) {
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
	s.Ym, s.Cl, s.Ok = y, c, true
}

func (s *Session) Wait() <-chan struct{} {
	s.RLock()
	defer s.RUnlock()
	return s.Ch
}

func (s *Session) Get() (*yamux.Session, bool) {
	s.RLock()
	defer s.RUnlock()
	return s.Ym, s.Ok
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
	s.Ym, s.Ok = nil, false
}

// --- Health + reconnect loops ---

func HealthLoop(ctx context.Context, sess *Session, rch chan<- struct{}) {
	log := getLog()
	tk := time.NewTicker(HealthEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			if y, ok := sess.Get(); ok && y != nil {
				if _, e := y.Ping(); e != nil {
					log.Warn("Connection lost")
					sess.Down()
					select {
					case rch <- struct{}{}:
					default:
					}
				}
			}
		}
	}
}

func ReconnectLoop(ctx context.Context, sess *Session, roomURL, pw string, isServer bool, rch <-chan struct{}, authValidator func(string) bool) {
	log := getLog()
	lis := getLis()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rch:
			lis.OnStatus("reconnecting")
			bo := 2 * time.Second
			for a := 1; ; a++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				log.Info(fmt.Sprintf("Reconnecting (#%d)...", a))
				y, c, e := Establish(roomURL, pw, isServer, authValidator)
				if e == nil {
					sess.Set(y, c)
					lis.OnStatus("connected")
					log.Info("Connection restored!")
					break
				}
				log.Warn(fmt.Sprintf("Attempt %d failed: %v", a, e))
				select {
				case <-ctx.Done():
					return
				case <-time.After(bo):
				}
				if bo *= 2; bo > MaxBackoff {
					bo = MaxBackoff
				}
			}
		}
	}
}

// SocksHandshake performs the initial SOCKS5 handshake and returns the target address.
func SocksHandshake(c net.Conn) (string, error) {
	buf := make([]byte, 258)
	// 1. Version + Methods
	if _, err := io.ReadFull(c, buf[:2]); err != nil || buf[0] != 0x05 {
		return "", fmt.Errorf("socks5 only")
	}
	n := int(buf[1])
	if n > 0 {
		if _, err := io.ReadFull(c, buf[:n]); err != nil {
			return "", err
		}
	}
	// No auth
	c.Write([]byte{0x05, 0x00})

	// 2. Request
	if _, err := io.ReadFull(c, buf[:4]); err != nil || buf[0] != 0x05 || buf[1] != 0x01 {
		return "", fmt.Errorf("socks5 connect only")
	}

	addrType := buf[3]
	var host string
	switch addrType {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 0x03: // Domain
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
	case 0x04: // IPv6
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

	// Reply success (dummy BND.ADDR)
	c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	return fmt.Sprintf("%s:%d", host, port), nil
}
