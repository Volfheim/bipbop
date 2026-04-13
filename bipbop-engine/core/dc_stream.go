package core

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// DCStream wraps WebRTCPeer DataChannel as net.Conn.
// Фрагментация 7168 байт — как в olcrtc mux.go (const chunkSize = 7168).
type DCStream struct {
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

func NewDCStream(peer *WebRTCPeer) *DCStream {
	s := &DCStream{
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

	// olcrtc mux.go uses chunkSize = 7168
	const chunkSize = 7168
	total := len(b)
	for i := 0; i < total; i += chunkSize {
		end := i + chunkSize
		if end > total {
			end = total
		}
		if s.peer.dc == nil {
			return 0, errors.New("datachannel not allocated")
		}

		err := s.peer.Send(b[i:end])
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
