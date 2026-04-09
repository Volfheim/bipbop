package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

type WebRTCPeer struct {
	roomURL     string
	name        string
	conn        *ConnectionInfo
	ws          *websocket.Conn
	wsMu        sync.Mutex
	pcSub       *webrtc.PeerConnection
	pcPub       *webrtc.PeerConnection
	dc          *webrtc.DataChannel
	onData      func([]byte)
	closeCh     chan struct{}
	keepAliveCh chan struct{}
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

	opts := &webrtc.DataChannelInit{
		Ordered: func(b bool) *bool { return &b }(true),
	}
	p.dc, err = p.pcPub.CreateDataChannel("bipbop", opts)
	if err != nil {
		return err
	}

	dcReady := make(chan struct{})
	p.dc.OnOpen(func() {
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

	ws, _, err := websocket.DefaultDialer.Dial(p.conn.ClientConfig.MediaServerURL, nil)
	if err != nil {
		return err
	}
	p.ws = ws

	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	go p.keepAlive()

	if err := p.sendHello(); err != nil {
		return err
	}

	p.setupICEHandlers()
	go p.handleSignaling()

	select {
	case <-dcReady:
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("datachannel connection timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *WebRTCPeer) Send(data []byte) error {
	if p.dc == nil {
		return fmt.Errorf("datachannel not ready")
	}
	return p.dc.Send(data)
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
				"userAgent":      "BipBopVPN-" + p.name,
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
			getLog().Warn(fmt.Sprintf("[RTC] WS read error: %v", err))
			return
		}

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
		}

		if offer, ok := msg["subscriberSdpOffer"].(map[string]interface{}); ok && !pubSent {
			sdp, _ := offer["sdp"].(string)
			pcSeq, _ := offer["pcSeq"].(float64)

			if err := p.pcSub.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer,
				SDP:  sdp,
			}); err == nil {
				if answer, err := p.pcSub.CreateAnswer(nil); err == nil {
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
				}
			}
			p.sendAck(uid)

			time.Sleep(300 * time.Millisecond)

			if pubOffer, err := p.pcPub.CreateOffer(nil); err == nil {
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
			candStr, _ := cand["candidate"].(string)
			target, _ := cand["target"].(string)
			sdpMid, _ := cand["sdpMid"].(string)
			sdpMLineIndex, _ := cand["sdpMlineIndex"].(float64)

			parts := strings.Fields(candStr)
			if len(parts) >= 8 {
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
		}
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
	select {
	case <-p.closeCh: // already closed
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
		case <-p.keepAliveCh:
			return
		case <-p.closeCh:
			return
		}
	}
}
