// Прямая копия из olcrtc/internal/telemost/peer.go
// websocket.DefaultDialer.Dial — БЕЗ кастомных диалеров.
package core

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

type WebRTCPeer struct {
	roomURL         string
	name            string
	conn            *ConnectionInfo
	ws              *websocket.Conn
	wsMu            sync.Mutex
	pcSub           *webrtc.PeerConnection
	pcPub           *webrtc.PeerConnection
	dc              *webrtc.DataChannel
	onData          func([]byte)
	closeCh         chan struct{}
	keepAliveCh     chan struct{}
	sendQueue       chan []byte
	sendQueueClosed atomic.Bool
	wg              sync.WaitGroup
	OnDisconnected  func()
}

func (p *WebRTCPeer) GetSendQueue() chan []byte {
	return p.sendQueue
}

func (p *WebRTCPeer) GetBufferedAmount() uint64 {
	if p.dc != nil {
		return p.dc.BufferedAmount()
	}
	return 0
}

func NewWebRTCPeer(roomURL, name string, onData func([]byte)) (*WebRTCPeer, error) {
	conn, err := GetConnectionInfo(roomURL, name)
	if err != nil {
		return nil, err
	}

	return &WebRTCPeer{
		roomURL:     roomURL,
		name:        name,
		conn:        conn,
		onData:      onData,
		closeCh:     make(chan struct{}),
		keepAliveCh: make(chan struct{}),
		sendQueue:   make(chan []byte, 5000),
	}, nil
}

func (p *WebRTCPeer) Connect(ctx context.Context) error {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.rtc.yandex.net:3478"}},
		},
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlan,
	}

	settingEngine := webrtc.SettingEngine{}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

	var err error
	p.pcSub, err = api.NewPeerConnection(config)
	if err != nil {
		return err
	}

	p.pcSub.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[RTC] Sub state change: %s", state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateClosed {
			if p.OnDisconnected != nil {
				p.OnDisconnected()
			}
		}
	})

	p.pcPub, err = api.NewPeerConnection(config)
	if err != nil {
		return err
	}

	p.pcPub.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[RTC] Pub state change: %s", state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateClosed {
			if p.OnDisconnected != nil {
				p.OnDisconnected()
			}
		}
	})

	p.dc, err = p.pcPub.CreateDataChannel("olcrtc", nil)
	if err != nil {
		return err
	}

	dcReady := make(chan struct{})
	p.dc.OnOpen(func() {
		log.Println("DataChannel opened")

		numWorkers := 4
		for i := 0; i < numWorkers; i++ {
			p.wg.Add(1)
			go func(workerID int) {
				defer p.wg.Done()
				p.processSendQueue(workerID)
			}(i)
		}

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.monitorQueue()
		}()

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.heartbeatLoop()
		}()

		close(dcReady)
	})

	p.dc.OnClose(func() {
		log.Println("DataChannel closed")
	})

	p.dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if p.onData != nil && len(msg.Data) > 0 {
			p.onData(msg.Data)
		}
	})

	p.pcSub.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Received datachannel: %s", dc.Label())
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if p.onData != nil && len(msg.Data) > 0 {
				p.onData(msg.Data)
			}
		})
	})

	// Используем кастомный диалер для обхода глушилок DNS
	wsDialer := websocket.Dialer{
		NetDialContext: (&SignalingDialer{}).DialContext,
		HandshakeTimeout: 30 * time.Second,
	}
	ws, _, err := wsDialer.Dial(p.conn.ClientConfig.MediaServerURL, nil)
	if err != nil {
		return err
	}
	p.ws = ws

	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	ws.SetReadDeadline(time.Now().Add(60 * time.Second))

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.keepAlive()
	}()

	if err := p.sendHello(); err != nil {
		return err
	}

	p.setupICEHandlers()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.handleSignaling()
	}()

	select {
	case <-dcReady:
		return nil
	case <-time.After(15 * time.Second):
		return fmt.Errorf("datachannel timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *WebRTCPeer) Send(data []byte) error {
	if p.dc == nil || p.dc.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("datachannel not ready")
	}

	if p.sendQueueClosed.Load() {
		return fmt.Errorf("send queue closed")
	}

	select {
	case p.sendQueue <- data:
		return nil
	case <-time.After(200 * time.Millisecond):
		queueLen := len(p.sendQueue)
		log.Printf("[SEND_QUEUE] Timeout! queue_len=%d, dropping packet size=%d", queueLen, len(data))
		return fmt.Errorf("send queue timeout")
	}
}

func (p *WebRTCPeer) CanSend() bool {
	queueLen := len(p.sendQueue)
	buffered := uint64(0)
	if p.dc != nil {
		buffered = p.dc.BufferedAmount()
	}
	// Увеличим порог для возможности отправки пульса
	return queueLen < 2000 && buffered < 5*1024*1024
}

func (p *WebRTCPeer) heartbeatLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	failCount := 0
	for {
		select {
		case <-p.closeCh:
			return
		case <-ticker.C:
			if p.dc != nil && p.dc.ReadyState() == webrtc.DataChannelStateOpen {
				ping := make([]byte, 12)
				if err := p.Send(ping); err != nil {
					failCount++
					log.Printf("[RTC] Heartbeat send failed (%d/3): %v", failCount, err)
					if failCount >= 3 {
						if p.OnDisconnected != nil {
							p.OnDisconnected()
						}
						return
					}
				} else {
					failCount = 0
				}
			}
		}
	}
}

func (p *WebRTCPeer) sendHello() error {
	hello := map[string]interface{}{
		"uid": uuid.New().String(),
		"hello": map[string]interface{}{
			"participantMeta": map[string]interface{}{
				"name":      p.name,
				"role":      "SPEAKER",
				"sendAudio": false,
				"sendVideo": false,
			},
			"participantAttributes": map[string]interface{}{
				"name": p.name,
				"role": "SPEAKER",
			},
			"sendAudio":         false,
			"sendVideo":         false,
			"sendSharing":       false,
			"participantId":     p.conn.PeerID,
			"roomId":            p.conn.RoomID,
			"serviceName":       "telemost",
			"credentials":       p.conn.Credentials,
			"capabilitiesOffer": map[string]interface{}{
				"offerAnswerMode":        []string{"SEPARATE"},
				"initialSubscriberOffer": []string{"ON_HELLO"},
				"slotsMode":              []string{"FROM_CONTROLLER"},
				"simulcastMode":          []string{"DISABLED"},
				"selfVadStatus":          []string{"FROM_SERVER"},
				"dataChannelSharing":     []string{"TO_RTP"},
			},
			"sdkInfo": map[string]interface{}{
				"implementation": "go",
				"version":        "1.0.0",
				"userAgent":      "OlcRTC-" + p.name,
			},
			"sdkInitializationId": uuid.New().String(),
			"disablePublisher":    false,
			"disableSubscriber":   false,
		},
	}

	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	return p.ws.WriteJSON(hello)
}

func (p *WebRTCPeer) handleSignaling() {
	pubSent := false

	for {
		var msg map[string]interface{}
		if err := p.ws.ReadJSON(&msg); err != nil {
			log.Printf("WS read error: %v", err)
			return
		}

		p.wsMu.Lock()
		if p.ws != nil {
			p.ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		}
		p.wsMu.Unlock()

		uid, _ := msg["uid"].(string)

		if _, ok := msg["serverHello"]; ok {
			p.sendAck(uid)
		}

		if _, ok := msg["updateDescription"]; ok {
			p.sendAck(uid)
		}

		if _, ok := msg["vadActivity"]; ok {
			p.sendAck(uid)
		}

		if _, ok := msg["ping"]; ok {
			p.sendPong(uid)
			continue
		}

		if _, ok := msg["pong"]; ok {
			p.sendAck(uid)
			continue
		}

		if offer, ok := msg["subscriberSdpOffer"].(map[string]interface{}); ok && !pubSent {
			sdp, _ := offer["sdp"].(string)
			pcSeq, _ := offer["pcSeq"].(float64)

			if err := p.pcSub.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer,
				SDP:  sdp,
			}); err != nil {
				log.Printf("SetRemoteDescription error: %v", err)
				continue
			}

			answer, err := p.pcSub.CreateAnswer(nil)
			if err != nil {
				log.Printf("CreateAnswer error: %v", err)
				continue
			}

			if err := p.pcSub.SetLocalDescription(answer); err != nil {
				log.Printf("SetLocalDescription error: %v", err)
				continue
			}

			p.wsMu.Lock()
			p.ws.WriteJSON(map[string]interface{}{
				"uid": uuid.New().String(),
				"subscriberSdpAnswer": map[string]interface{}{
					"pcSeq": int(pcSeq),
					"sdp":   answer.SDP,
				},
			})
			p.wsMu.Unlock()

			p.sendAck(uid)
			time.Sleep(300 * time.Millisecond)

			pubOffer, err := p.pcPub.CreateOffer(nil)
			if err != nil {
				log.Printf("CreateOffer error: %v", err)
				continue
			}

			if err := p.pcPub.SetLocalDescription(pubOffer); err != nil {
				log.Printf("SetLocalDescription error: %v", err)
				continue
			}

			p.wsMu.Lock()
			p.ws.WriteJSON(map[string]interface{}{
				"uid": uuid.New().String(),
				"publisherSdpOffer": map[string]interface{}{
					"pcSeq": 1,
					"sdp":   pubOffer.SDP,
				},
			})
			p.wsMu.Unlock()

			pubSent = true
		}

		if answer, ok := msg["publisherSdpAnswer"].(map[string]interface{}); ok {
			sdp, _ := answer["sdp"].(string)

			if err := p.pcPub.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeAnswer,
				SDP:  sdp,
			}); err != nil {
				log.Printf("SetRemoteDescription error: %v", err)
			}

			p.sendAck(uid)
		}

		if cand, ok := msg["webrtcIceCandidate"].(map[string]interface{}); ok {
			p.handleICE(cand)
		}
	}
}

func (p *WebRTCPeer) handleICE(cand map[string]interface{}) {
	candStr, _ := cand["candidate"].(string)
	target, _ := cand["target"].(string)
	sdpMid, _ := cand["sdpMid"].(string)
	sdpMLineIndex, _ := cand["sdpMlineIndex"].(float64)

	parts := strings.Fields(candStr)
	if len(parts) < 8 {
		return
	}

	init := webrtc.ICECandidateInit{
		Candidate:     candStr,
		SDPMid:        &sdpMid,
		SDPMLineIndex: func() *uint16 { v := uint16(sdpMLineIndex); return &v }(),
	}

	if target == "SUBSCRIBER" {
		p.pcSub.AddICECandidate(init)
	} else if target == "PUBLISHER" {
		p.pcPub.AddICECandidate(init)
	}
}

func (p *WebRTCPeer) sendAck(uid string) {
	p.wsMu.Lock()
	defer p.wsMu.Unlock()

	p.ws.WriteJSON(map[string]interface{}{
		"uid": uid,
		"ack": map[string]interface{}{
			"status": map[string]interface{}{
				"code": "OK",
			},
		},
	})
}

func (p *WebRTCPeer) sendPong(uid string) {
	p.wsMu.Lock()
	defer p.wsMu.Unlock()

	p.ws.WriteJSON(map[string]interface{}{
		"uid":  uid,
		"pong": map[string]interface{}{},
	})
}

func (p *WebRTCPeer) setupICEHandlers() {
	p.pcSub.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}

		init := c.ToJSON()
		p.wsMu.Lock()
		p.ws.WriteJSON(map[string]interface{}{
			"uid": uuid.New().String(),
			"webrtcIceCandidate": map[string]interface{}{
				"candidate":     init.Candidate,
				"sdpMid":        init.SDPMid,
				"sdpMlineIndex": init.SDPMLineIndex,
				"target":        "SUBSCRIBER",
				"pcSeq":         1,
			},
		})
		p.wsMu.Unlock()
	})

	p.pcPub.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}

		init := c.ToJSON()
		p.wsMu.Lock()
		p.ws.WriteJSON(map[string]interface{}{
			"uid": uuid.New().String(),
			"webrtcIceCandidate": map[string]interface{}{
				"candidate":     init.Candidate,
				"sdpMid":        init.SDPMid,
				"sdpMlineIndex": init.SDPMLineIndex,
				"target":        "PUBLISHER",
				"pcSeq":         1,
			},
		})
		p.wsMu.Unlock()
	})
}

func (p *WebRTCPeer) Close() error {
	log.Println("Closing peer connection...")

	p.sendQueueClosed.Store(true)

	select {
	case <-p.closeCh:
	default:
		close(p.closeCh)
	}

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	if p.dc != nil {
		p.dc.Close()
	}
	if p.pcPub != nil {
		p.pcPub.Close()
	}
	if p.pcSub != nil {
		p.pcSub.Close()
	}
	if p.ws != nil {
		p.wsMu.Lock()
		p.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		p.ws.Close()
		p.wsMu.Unlock()
	}

	return nil
}

func (p *WebRTCPeer) Wait() <-chan struct{} {
	return p.closeCh
}

func (p *WebRTCPeer) keepAlive() {
	wsPingTicker := time.NewTicker(30 * time.Second)
	defer wsPingTicker.Stop()

	appPingTicker := time.NewTicker(5 * time.Second)
	defer appPingTicker.Stop()

	for {
		select {
		case <-wsPingTicker.C:
			p.wsMu.Lock()
			if p.ws != nil {
				if err := p.ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
					log.Printf("WS Ping error: %v", err)
					p.wsMu.Unlock()
					return
				}
			}
			p.wsMu.Unlock()
		case <-appPingTicker.C:
			p.wsMu.Lock()
			if p.ws != nil {
				if err := p.ws.WriteJSON(map[string]interface{}{
					"uid":  uuid.New().String(),
					"ping": map[string]interface{}{},
				}); err != nil {
					log.Printf("App Ping error: %v", err)
					p.wsMu.Unlock()
					return
				}
			}
			p.wsMu.Unlock()
		case <-p.keepAliveCh:
			return
		case <-p.closeCh:
			return
		}
	}
}

func (p *WebRTCPeer) processSendQueue(workerID int) {
	for {
		select {
		case data := <-p.sendQueue:
			if p.dc == nil || p.dc.ReadyState() != webrtc.DataChannelStateOpen {
				continue
			}

			start := time.Now()

			for p.dc.BufferedAmount() > 4*1024*1024 {
				time.Sleep(10 * time.Millisecond)
				if time.Since(start) > 30*time.Second {
					log.Printf("[WORKER-%d] Buffer wait timeout, dropping packet size=%d", workerID, len(data))
					break
				}
			}

			if time.Since(start) > 30*time.Second {
				continue
			}

			if err := p.dc.Send(data); err != nil {
				log.Printf("[WORKER-%d] Send error: %v", workerID, err)
			}

		case <-p.closeCh:
			return
		}
	}
}

func (p *WebRTCPeer) monitorQueue() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			queueLen := len(p.sendQueue)
			buffered := uint64(0)
			if p.dc != nil {
				buffered = p.dc.BufferedAmount()
			}
			if queueLen > 800 || buffered > 3*1024*1024 {
				log.Printf("[QUEUE_MONITOR] queue_len=%d dc_buffered=%d MB", queueLen, buffered/(1024*1024))
			}
		case <-p.closeCh:
			return
		}
	}
}
