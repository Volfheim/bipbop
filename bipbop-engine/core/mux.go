package core

import (
	"encoding/binary"
	"fmt"
	"sync"
)

// Stream представляет собой отдельный TCP-поток внутри DataChannel
type MuxStream struct {
	ID          uint16
	ClientID    uint32
	recvBuf     []byte
	closed      bool
	mu          sync.Mutex
	nextSeq     uint32
	outOfOrder  map[uint32][]byte
}

func (s *MuxStream) RecvBuf() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recvBuf
}

// Multiplexer — легковесный мультиплексор из olcrtc
type Multiplexer struct {
	streams       map[uint16]*MuxStream
	nextID        uint16
	clientID      uint32
	onSend        func([]byte) error
	mu            sync.RWMutex
	maxStreams    int
	maxBufferSize int
	dataReady     map[uint16]chan struct{}
	dataReadyMu   sync.Mutex
	sendSeq       map[uint16]uint32
	sendSeqMu     sync.Mutex
	acceptCh      chan uint16
}

func NewMultiplexer(clientID uint32, onSend func([]byte) error) *Multiplexer {
	return &Multiplexer{
		streams:       make(map[uint16]*MuxStream),
		nextID:        1,
		clientID:      clientID,
		onSend:        onSend,
		maxStreams:    10000,
		maxBufferSize: 16 * 1024 * 1024,
		dataReady:     make(map[uint16]chan struct{}),
		sendSeq:       make(map[uint16]uint32),
		acceptCh:      make(chan uint16, 1000),
	}
}

func (m *Multiplexer) OpenStream() uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()

	for {
		sid := m.nextID
		m.nextID++
		if m.nextID == 0 {
			m.nextID = 1
		}
		
		if _, exists := m.streams[sid]; !exists {
			m.streams[sid] = &MuxStream{
				ID:         sid,
				recvBuf:    make([]byte, 0),
				nextSeq:    0,
				outOfOrder: make(map[uint32][]byte),
			}
			return sid
		}
	}
}

func (m *Multiplexer) SendData(sid uint16, data []byte) error {
	m.mu.RLock()
	stream, exists := m.streams[sid]
	m.mu.RUnlock()

	if !exists || stream.closed {
		return nil
	}

	const chunkSize = 7168
	
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}

		chunk := data[i:end]
		
		m.sendSeqMu.Lock()
		seq := m.sendSeq[sid]
		m.sendSeq[sid]++
		m.sendSeqMu.Unlock()
		
		frame := make([]byte, 12+len(chunk))
		binary.BigEndian.PutUint32(frame[0:4], m.clientID)
		binary.BigEndian.PutUint16(frame[4:6], sid)
		binary.BigEndian.PutUint16(frame[6:8], uint16(len(chunk)))
		binary.BigEndian.PutUint32(frame[8:12], seq)
		copy(frame[12:], chunk)

		if err := m.onSend(frame); err != nil {
			return err
		}
	}

	return nil
}

func (m *Multiplexer) CloseStream(sid uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if stream, exists := m.streams[sid]; exists {
		stream.closed = true
	}
	
	m.sendSeqMu.Lock()
	delete(m.sendSeq, sid)
	m.sendSeqMu.Unlock()

	frame := make([]byte, 12)
	binary.BigEndian.PutUint32(frame[0:4], m.clientID)
	binary.BigEndian.PutUint16(frame[4:6], sid)
	binary.BigEndian.PutUint16(frame[6:8], 0)
	binary.BigEndian.PutUint32(frame[8:12], 0)

	return m.onSend(frame)
}

func (m *Multiplexer) HandleFrame(frame []byte) {
	if len(frame) < 12 {
		return
	}

	clientID := binary.BigEndian.Uint32(frame[0:4])
	sid := binary.BigEndian.Uint16(frame[4:6])
	length := binary.BigEndian.Uint16(frame[6:8])
	seq := binary.BigEndian.Uint32(frame[8:12])

	// 0xFFFF 0xFFFF — сигнал от сервера о закрытии всех стримов клиента
	if sid == 0xFFFF && length == 0xFFFF {
		m.mu.Lock()
		for streamSid, stream := range m.streams {
			if stream.ClientID == clientID {
				stream.closed = true
				delete(m.streams, streamSid)
			}
		}
		m.mu.Unlock()
		return
	}

	// length 0 — сигнал о закрытии конкретного стрима
	if length == 0 {
		m.mu.Lock()
		if stream, exists := m.streams[sid]; exists && stream.ClientID == clientID {
			stream.closed = true
		}
		m.mu.Unlock()
		return
	}

	if len(frame) < 12+int(length) {
		return
	}

	data := frame[12 : 12+length]

	m.mu.Lock()
	defer m.mu.Unlock()
	
	stream, exists := m.streams[sid]
	if !exists {
		if len(m.streams) >= m.maxStreams {
			return
		}
		stream = &MuxStream{
			ID:         sid,
			ClientID:   clientID,
			recvBuf:    make([]byte, 0),
			nextSeq:    0,
			outOfOrder: make(map[uint32][]byte),
		}
		m.streams[sid] = stream

		// Уведомляем сервер о новом потоке
		select {
		case m.acceptCh <- sid:
		default:
		}
	} else if stream.ClientID != clientID {
		stream.ClientID = clientID
		stream.recvBuf = make([]byte, 0)
		stream.closed = false
		stream.nextSeq = 0
		stream.outOfOrder = make(map[uint32][]byte)
	}
	
	if seq == stream.nextSeq {
		if len(stream.recvBuf)+len(data) > m.maxBufferSize {
			stream.closed = true
			return
		}
		stream.recvBuf = append(stream.recvBuf, data...)
		stream.nextSeq++
		
		for {
			if nextData, ok := stream.outOfOrder[stream.nextSeq]; ok {
				if len(stream.recvBuf)+len(nextData) > m.maxBufferSize {
					stream.closed = true
					return
				}
				stream.recvBuf = append(stream.recvBuf, nextData...)
				delete(stream.outOfOrder, stream.nextSeq)
				stream.nextSeq++
			} else {
				break
			}
		}
		
		m.dataReadyMu.Lock()
		if ch, ok := m.dataReady[sid]; ok {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		m.dataReadyMu.Unlock()
	} else if seq > stream.nextSeq {
		if len(stream.outOfOrder) < 100 {
			stream.outOfOrder[seq] = append([]byte(nil), data...)
		}
	}
}

func (m *Multiplexer) ReadStream(sid uint16) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	stream, exists := m.streams[sid]
	if !exists || len(stream.recvBuf) == 0 {
		return nil
	}

	data := stream.recvBuf
	stream.recvBuf = make([]byte, 0)
	return data
}

func (m *Multiplexer) StreamClosed(sid uint16) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stream, exists := m.streams[sid]
	return !exists || stream.closed
}

func (m *Multiplexer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, stream := range m.streams {
		stream.closed = true
	}
	
	m.streams = make(map[uint16]*MuxStream)
	m.nextID = 1
	
	m.sendSeqMu.Lock()
	m.sendSeq = make(map[uint16]uint32)
	m.sendSeqMu.Unlock()
}

func (m *Multiplexer) WaitForData(sid uint16) <-chan struct{} {
	m.dataReadyMu.Lock()
	defer m.dataReadyMu.Unlock()
	
	if _, ok := m.dataReady[sid]; !ok {
		m.dataReady[sid] = make(chan struct{}, 1)
	}
	return m.dataReady[sid]
}

func (m *Multiplexer) AcceptStream() (uint16, error) {
	select {
	case sid := <-m.acceptCh:
		return sid, nil
	case <-time.After(1 * time.Second):
		return 0, fmt.Errorf("timeout")
	}
}

func (m *Multiplexer) CleanupStream(sid uint16) {
	m.dataReadyMu.Lock()
	defer m.dataReadyMu.Unlock()
	
	if ch, ok := m.dataReady[sid]; ok {
		close(ch)
		delete(m.dataReady, sid)
	}

	m.mu.Lock()
	delete(m.streams, sid)
	m.mu.Unlock()
}

// Пакет core также реализует адаптер для net.Conn поверх MuxStream (для серверной части)
type MuxConn struct {
	sid   uint16
	mux   *Multiplexer
	isCl  bool
	lAddr AddrMock
	rAddr AddrMock
}

func NewMuxConn(sid uint16, mux *Multiplexer) *MuxConn {
	return &MuxConn{sid: sid, mux: mux, lAddr: AddrMock{"local-mux"}, rAddr: AddrMock{"remote-mux"}}
}

func (c *MuxConn) Read(b []byte) (int, error) {
	for {
		data := c.mux.ReadStream(c.sid)
		if data != nil {
			return copy(b, data), nil
		}
		if c.mux.StreamClosed(c.sid) {
			return 0, fmt.Errorf("EOF")
		}
		select {
		case <-c.mux.WaitForData(c.sid):
		}
	}
}

func (c *MuxConn) Write(b []byte) (int, error) {
	err := c.mux.SendData(c.sid, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *MuxConn) Close() error {
	c.isCl = true
	c.mux.CloseStream(c.sid)
	c.mux.CleanupStream(c.sid)
	return nil
}

func (c *MuxConn) LocalAddr() net.Addr                { return c.lAddr }
func (c *MuxConn) RemoteAddr() net.Addr               { return c.rAddr }
func (c *MuxConn) SetDeadline(t time.Time) error      { return nil }
func (c *MuxConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *MuxConn) SetWriteDeadline(t time.Time) error { return nil }
