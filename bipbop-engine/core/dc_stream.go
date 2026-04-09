package core

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// DCStream wraps webrtc.DataChannel to act exactly like standard net.Conn.
// It fragments outgoing data to bypass Yandex 8KB message size limit.
type DCStream struct {
	dc         *webrtc.DataChannel
	peer       *WebRTCPeer
	readBuf    bytes.Buffer
	readMu     sync.Mutex
	readCond   *sync.Cond
	isClosed   bool
	localAddr  AddrMock
	remoteAddr AddrMock
}

type AddrMock struct {
	addr string
}

func (a AddrMock) Network() string { return "webrtc" }
func (a AddrMock) String() string  { return a.addr }

// NewDCStream creates a streaming adapter over WebRTC DataChannel
func NewDCStream(peer *WebRTCPeer) *DCStream {
	s := &DCStream{
		dc:         peer.dc,
		peer:       peer,
		localAddr:  AddrMock{addr: "local-webrtc"},
		remoteAddr: AddrMock{addr: "remote-webrtc"},
	}
	s.readCond = sync.NewCond(&s.readMu)

	// Callback from webrtc.go triggers this
	peer.onData = s.pushData

	return s
}

func (s *DCStream) pushData(data []byte) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.isClosed {
		return
	}
	s.readBuf.Write(data)
	s.readCond.Signal()
}

func (s *DCStream) Read(b []byte) (n int, err error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()

	for s.readBuf.Len() == 0 && !s.isClosed {
		s.readCond.Wait()
	}

	if s.readBuf.Len() > 0 {
		return s.readBuf.Read(b)
	}

	if s.isClosed {
		return 0, io.EOF
	}

	return 0, errors.New("read error in dc stream")
}

func (s *DCStream) Write(b []byte) (n int, err error) {
	if s.isClosed {
		return 0, errors.New("stream closed")
	}

	// Yandex Telemost silently drops packets > 8KB.
	// Fragment anything larger into 8000 byte chunks.
	const chunkSize = 8000
	total := len(b)
	for i := 0; i < total; i += chunkSize {
		end := i + chunkSize
		if end > total {
			end = total
		}
		if s.dc == nil {
			return 0, errors.New("datachannel not allocated")
		}

		err := s.dc.Send(b[i:end])
		if err != nil {
			return i, err
		}
	}

	return total, nil
}

func (s *DCStream) Close() error {
	s.readMu.Lock()
	if s.isClosed {
		s.readMu.Unlock()
		return nil
	}
	s.isClosed = true
	s.readCond.Broadcast()
	s.readMu.Unlock()

	return s.peer.Close()
}

func (s *DCStream) LocalAddr() net.Addr {
	return s.localAddr
}

func (s *DCStream) RemoteAddr() net.Addr {
	return s.remoteAddr
}

func (s *DCStream) SetDeadline(t time.Time) error {
	return nil
}

func (s *DCStream) SetReadDeadline(t time.Time) error {
	return nil
}

func (s *DCStream) SetWriteDeadline(t time.Time) error {
	return nil
}
