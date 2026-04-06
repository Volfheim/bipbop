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
	"github.com/pion/turn/v4"
	"github.com/xtaci/kcp-go/v5"
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

func (nopStatus) OnStatus(string)     {}
func (nopStatus) OnTurnInfo(string)   {}
func (nopStatus) OnStats(int64, int64) {}

// --- globals set by the host ---

var (
	mu  sync.Mutex
	Log Logger         = nopLogger{}
	Lis StatusListener = nopStatus{}
)

func SetLogger(l Logger)         { mu.Lock(); Log = l; mu.Unlock() }
func SetListener(l StatusListener) { mu.Lock(); Lis = l; mu.Unlock() }

func getLog() Logger           { mu.Lock(); defer mu.Unlock(); return Log }
func getLis() StatusListener   { mu.Lock(); defer mu.Unlock(); return Lis }

// --- Key derivation ---

func DeriveKey(pw string) []byte { h := sha256.Sum256([]byte(pw)); return h[:] }

// --- Smart-key ---

func ParseSmartKey(k string) (peer, pw string, err error) {
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
	peer = parts[0]
	if !strings.Contains(peer, ":") {
		peer += ":" + DefPort
	}
	pw = parts[1]
	return
}

func EncodeSmartKey(ip, port, password string) string {
	raw := ip + ":" + port + "|" + password
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func SmartKeyServerIP(k string) (string, error) {
	peer, _, err := ParseSmartKey(k)
	if err != nil {
		return "", err
	}
	host, _, _ := net.SplitHostPort(peer)
	return host, nil
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

// --- TURN dial ---

func DialTURN(cred TurnCred, peer, pw string) (*yamux.Session, io.Closer, error) {
	addr := strings.TrimPrefix(strings.TrimPrefix(strings.Split(cred.URL, "?")[0], "turn:"), "turns:")
	uc, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, nil, err
	}
	tc, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: addr,
		TURNServerAddr: addr,
		Conn:           uc,
		Username:       cred.User,
		Password:       cred.Pass,
	})
	if err != nil {
		uc.Close()
		return nil, nil, err
	}
	if err := tc.Listen(); err != nil {
		tc.Close()
		uc.Close()
		return nil, nil, err
	}
	relay, err := tc.Allocate()
	if err != nil {
		tc.Close()
		uc.Close()
		return nil, nil, err
	}
	blk, _ := kcp.NewAESBlockCrypt(DeriveKey(pw))
	kc, err := kcp.NewConn(peer, blk, 10, 3, relay)
	if err != nil {
		tc.Close()
		uc.Close()
		return nil, nil, err
	}
	kc.SetNoDelay(0, 40, 0, 0)
	kc.SetWindowSize(64, 64)
	kc.SetStreamMode(true)
	ym, err := yamux.Client(kc, YmxCfg())
	if err != nil {
		kc.Close()
		tc.Close()
		uc.Close()
		return nil, nil, err
	}
	return ym, &MultiCloser{[]io.Closer{ym, kc, CloserFunc(func() { tc.Close() }), uc}}, nil
}

// --- Establish tunnel (try all TURN servers) ---

func Establish(cache *CredsCache, peer, pw string, force bool) (*yamux.Session, io.Closer, error) {
	creds, err := cache.Get(force)
	if err != nil {
		return nil, nil, err
	}
	var turnCreds []TurnCred
	for _, c := range creds {
		if strings.HasPrefix(c.URL, "turn") {
			turnCreds = append(turnCreds, c)
		}
	}
	if len(turnCreds) == 0 {
		return nil, nil, fmt.Errorf("no TURN servers found")
	}

	log := getLog()
	
	type result struct {
		ym  *yamux.Session
		cl  io.Closer
		err error
	}
	
	winner := make(chan result, 1)
	failedCount := atomic.Int32{}
	total := int32(len(turnCreds))
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, c := range turnCreds {
		go func(cr TurnCred) {
			ym, cl, err := DialTURN(cr, peer, pw)
			if err != nil {
				if int(failedCount.Add(1)) == int(total) {
					select {
					case winner <- result{err: err}:
					default:
					}
				}
				return
			}
			
			// Ping check
			pingCh := make(chan error, 1)
			go func() { _, e := ym.Ping(); pingCh <- e }()
			
			var pingErr error
			select {
			case pingErr = <-pingCh:
			case <-time.After(5 * time.Second):
				pingErr = fmt.Errorf("ping timeout")
			case <-ctx.Done():
				cl.Close()
				return
			}

			if pingErr == nil {
				select {
				case winner <- result{ym: ym, cl: cl}:
					log.Info(fmt.Sprintf("[CORE] TURN server won the race: %s", cr.URL))
				case <-ctx.Done():
					cl.Close()
				}
			} else {
				cl.Close()
				if int(failedCount.Add(1)) == int(total) {
					select {
					case winner <- result{err: pingErr}:
					default:
					}
				}
			}
		}(c)
	}

	res := <-winner
	if res.err != nil {
		return nil, nil, fmt.Errorf("all TURN servers unreachable: %v", res.err)
	}
	return res.ym, res.cl, nil
}

// --- Session wrapper ---

type Session struct {
	sync.RWMutex
	Ym *yamux.Session
	Cl io.Closer
	Ch chan struct{}
	Ok bool
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

func ReconnectLoop(ctx context.Context, sess *Session, cache *CredsCache, peer, pw string, rch <-chan struct{}) {
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
				y, c, e := Establish(cache, peer, pw, a > 3)
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
		if _, err := io.ReadFull(c, b); err != nil { return "", err }
		host = net.IP(b).String()
	case 0x03: // Domain
		b := make([]byte, 1)
		if _, err := io.ReadFull(c, b); err != nil { return "", err }
		sz := int(b[0])
		db := make([]byte, sz)
		if _, err := io.ReadFull(c, db); err != nil { return "", err }
		host = string(db)
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil { return "", err }
		host = net.IP(b).String()
	default:
		return "", fmt.Errorf("unsupported addr type %d", addrType)
	}
	
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil { return "", err }
	port := int(pb[0])<<8 | int(pb[1])
	
	// Reply success (dummy BND.ADDR)
	c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	
	return fmt.Sprintf("%s:%d", host, port), nil
}
