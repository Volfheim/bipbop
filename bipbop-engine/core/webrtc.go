package core

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

// Пул IP-адресов для goloom.strm.yandex.net
var mediaIPs = []string{
	"87.250.254.244",
	"87.250.250.12",
	"213.180.204.244",
}

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

	p.pcPub, err = api.NewPeerConnection(config)
	if err != nil {
		return err
	}

	p.dc, err = p.pcPub.CreateDataChannel("olcrtc", nil)
	if err != nil {
		return err
	}

	dcReady := make(chan struct{})
	p.dc.OnOpen(func() {
		getLog().Info("[RTC] DataChannel opened")
		numWorkers := 4
		for i := 0; i < numWorkers; i++ {
			p.wg.Add(1)
			go func(id int) {
				defer p.wg.Done()
				p.processSendQueue(id)
			}(i)
		}
		close(dcReady)
	})

	p.dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if p.onData != nil && len(msg.Data) > 0 {
			p.onData(msg.Data)
		}
	})

	p.pcSub.OnDataChannel(func(dc *webrtc.DataChannel) {
		getLog().Info(fmt.Sprintf("[RTC] Received DataChannel: %s", dc.Label()))
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if p.onData != nil && len(msg.Data) > 0 {
				p.onData(msg.Data)
			}
		})
	})

	// Применяем FragDialer + IP Fallback для WebSocket-соединения в гипер-режиме
	fd := &FragDialer{
		Dialer: net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, _ := net.SplitHostPort(addr)

			// 1. Стандарт + Фрагментация
			conn, err := fd.DialContext(ctx, "tcp4", addr)
			if err == nil {
				return conn, nil
			}

			// 2. Резервный пул IP + Фрагментация
			if host == "goloom.strm.yandex.net" {
				getLog().Warn(fmt.Sprintf("[RTC] DNS failed for %s, trying fallback IPs + Fragmentation...", host))
				for _, ip := range mediaIPs {
					conn, err = fd.DialContext(ctx, "tcp4", net.JoinHostPort(ip, port))
					if err == nil {
						getLog().Info(fmt.Sprintf("[RTC] Connected to Media via IP + frag: %s", ip))
						return conn, nil
					}
				}
			}
			return nil, err
		},
	}

	ws, _, err := dialer.Dial(p.conn.ClientConfig.MediaServerURL, nil)
	if err != nil {
		return fmt.Errorf("media ws Hyper-Dial error: %w", err)
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
	case <-time.After(30 * time.Second):
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
	case <-time.After(50 * time.Millisecond):
		return fmt.Errorf("send queue timeout")
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
			"sendAudio":     false,
			"sendVideo":     false,
			"sendSharing":   false,
			"participantId": p.conn.PeerID,
			"roomId":        p.conn.RoomID,
			"serviceName":   "telemost",
			"credentials":   p.conn.Credentials,
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

			p.pcSub.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer,
				SDP:  sdp,
			})
			answer, _ := p.pcSub.CreateAnswer(nil)
			p.pcSub.SetLocalDescription(answer)

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

			pubOffer, _ := p.pcPub.CreateOffer(nil)
			p.pcPub.SetLocalDescription(pubOffer)
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
			p.pcPub.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeAnswer,
				SDP:  sdp,
			})
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

	if !strings.Contains(candStr, "candidate") {
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
		"ack": map[string]interface{}{"status": map[string]interface{}{"code": "OK"}},
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
	p.sendQueueClosed.Store(true)
	select {
	case <-p.closeCh:
	default:
		close(p.closeCh)
	}
	if p.ws != nil {
		p.ws.Close()
	}
	if p.pcSub != nil {
		p.pcSub.Close()
	}
	if p.pcPub != nil {
		p.pcPub.Close()
	}
	return nil
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
				p.ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
			}
			p.wsMu.Unlock()
		case <-appPingTicker.C:
			p.wsMu.Lock()
			if p.ws != nil {
				p.ws.WriteJSON(map[string]interface{}{
					"uid":  uuid.New().String(),
					"ping": map[string]interface{}{},
				})
			}
			p.wsMu.Unlock()
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
			// Wait if buffer is too large
			for p.dc.BufferedAmount() > 4*1024*1024 {
				time.Sleep(10 * time.Millisecond)
			}
			p.dc.Send(data)
		case <-p.closeCh:
			return
		}
	}
}
