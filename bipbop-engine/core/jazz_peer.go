// package core implements the SaluteJazz WebRTC provider.
package core

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

const (
	maxDataChannelMessageSize = 12288
	sendDelay                 = 2 * time.Millisecond
)

// Peer represents a SaluteJazz WebRTC connection.
type Peer struct {
	name            string
	roomInfo        *RoomInfo
	ws              *websocket.Conn
	wsMu            sync.Mutex
	pcSub           *webrtc.PeerConnection
	pcPub           *webrtc.PeerConnection
	dc              *webrtc.DataChannel
	onData          func([]byte)
	onReconnect     func(*webrtc.DataChannel)
	shouldReconnect func() bool
	reconnectCh     chan struct{}
	closeCh         chan struct{}
	closed          atomic.Bool
	reconnecting    atomic.Bool
	sendQueue       chan []byte
	sendQueueClosed atomic.Bool
	onEnded         func(string)
	sessionCloseCh  chan struct{}
	wg              sync.WaitGroup
	groupID         string
	videoTrack      *webrtc.TrackLocalStaticSample
	videoTrackSub   *webrtc.TrackRemote
}

// NewPeer creates a new Jazz provider peer.
func NewPeer(ctx context.Context, roomID, name string, onData func([]byte)) (*Peer, error) {
	var roomInfo *RoomInfo
	var err error

	if roomID == "" || roomID == "any" || roomID == "dummy" {
		roomInfo, err = CreateRoom(ctx)
		if err != nil {
			return nil, fmt.Errorf("create room: %w", err)
		}
		log.Printf("Jazz room created: %s:%s", roomInfo.RoomID, roomInfo.Password)
	} else {
		var password string
		parts := strings.Split(roomID, ":")
		if len(parts) == 2 {
			roomID = parts[0]
			password = parts[1]
		}

		roomInfo, err = joinRoom(ctx, roomID, password)
		if err != nil {
			return nil, fmt.Errorf("join room: %w", err)
		}
	}

	return &Peer{
		name:           name,
		roomInfo:       roomInfo,
		onData:         onData,
		reconnectCh:    make(chan struct{}, 1),
		closeCh:        make(chan struct{}),
		sessionCloseCh: make(chan struct{}),
		sendQueue:      make(chan []byte, 5000),
	}, nil
}

// Connect starts the WebRTC connection process.
func (p *Peer) Connect(ctx context.Context) error {
	p.closed.Store(false)

	config := webrtc.Configuration{
		ICEServers:   []webrtc.ICEServer{},
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlan,
		BundlePolicy: webrtc.BundlePolicyMaxBundle,
	}

	settingEngine := webrtc.SettingEngine{}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

	var err error
	p.pcSub, err = api.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("create subscriber pc: %w", err)
	}

	p.pcPub, err = api.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("create publisher pc: %w", err)
	}

	p.videoTrack, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "bipbop")
	if err != nil {
		return fmt.Errorf("create video track: %w", err)
	}

	if _, err = p.pcPub.AddTrack(p.videoTrack); err != nil {
		return fmt.Errorf("add video track: %w", err)
	}

	p.pcSub.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			log.Printf("[Jazz] Received video track: %s", track.ID())
			p.videoTrackSub = track
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.handleIncomingVideoTrack(track)
			}()
		}
	})

	p.dc, err = p.pcPub.CreateDataChannel("_reliable", &webrtc.DataChannelInit{
		Ordered: func() *bool { v := true; return &v }(),
	})
	if err != nil {
		return fmt.Errorf("create datachannel: %w", err)
	}

	dcReady := make(chan struct{})
	p.setupDataChannelHandlers(dcReady)

	if err := p.dialWebSocket(); err != nil {
		return err
	}

	if err := p.sendJoin(); err != nil {
		return err
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.handleSignaling(ctx)
	}()

	select {
	case <-dcReady:
		log.Printf("[Jazz] Connection established!")
		return nil
	case <-time.After(30 * time.Second):
		return errors.New("datachannel timeout")
	case <-ctx.Done():
		return fmt.Errorf("connect cancelled: %w", ctx.Err())
	}
}

func (p *Peer) dialWebSocket() error {
	wsDialer := websocket.Dialer{
		NetDialContext:   (&SignalingDialer{}).DialContext,
		HandshakeTimeout: 15 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}

	header := http.Header{}
	header.Add("Origin", "https://bk.salutejazz.ru")
	header.Add("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	ws, resp, err := wsDialer.Dial(p.roomInfo.ConnectorURL, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial websocket: %w (Status: %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("dial websocket: %w", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	p.ws = ws
	return nil
}

func (p *Peer) sendJoin() error {
	joinMsg := map[string]any{
		"roomId":    p.roomInfo.RoomID,
		"event":     "join",
		"requestId": uuid.New().String(),
		"payload": map[string]any{
			"password":        p.roomInfo.Password,
			"participantName": p.name,
			"supportedFeatures": map[string]any{
				"attachedRooms": true,
				"sessionGroups": true,
			},
			"isSilent":  false,
			"sendAudio": true,
			"sendVideo": true,
		},
	}

	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	return p.ws.WriteJSON(joinMsg)
}

func (p *Peer) setupDataChannelHandlers(dcReady chan struct{}) {
	p.dc.OnOpen(func() {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.processSendQueue()
		}()
		close(dcReady)
	})

	p.dc.OnClose(func() {
		if !p.closed.Load() {
			p.queueReconnect()
		}
	})

	p.dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		p.handleIncomingMessage(msg.Data, "publisher")
	})

	p.pcSub.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == "_reliable" {
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				p.handleIncomingMessage(msg.Data, "subscriber")
			})
		}
	})
}

func (p *Peer) handleIncomingMessage(data []byte, source string) {
	payload, ok := DecodeDataPacket(data)
	if !ok {
		if p.onData != nil && len(data) > 0 {
			p.onData(data)
		}
		return
	}
	if p.onData != nil && len(payload) > 0 {
		p.onData(payload)
	}
}

func (p *Peer) handleIncomingVideoTrack(track *webrtc.TrackRemote) {
	for {
		rtp, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		if len(rtp.Payload) > 0 {
			p.handleIncomingMessage(rtp.Payload, "video-track")
		}
	}
}

func (p *Peer) handleSignaling(_ context.Context) {
	for {
		var msg map[string]any
		if err := p.ws.ReadJSON(&msg); err != nil {
			if !p.closed.Load() {
				p.queueReconnect()
			}
			return
		}

		event, _ := msg["event"].(string)
		payload, _ := msg["payload"].(map[string]any)

		switch event {
		case "join-response":
			group, _ := payload["participantGroup"].(map[string]any)
			p.groupID, _ = group["groupId"].(string)
		case "media-out":
			method, _ := payload["method"].(string)
			switch method {
			case "rtc:config":
				p.handleRTCConfig(payload)
			case "rtc:offer":
				p.handleSubscriberOffer(payload)
			case "rtc:answer":
				p.handlePublisherAnswer(payload)
			case "rtc:ice":
				p.handleICE(payload)
			}
		}
	}
}

func (p *Peer) handleRTCConfig(payload map[string]any) {
	config, _ := payload["configuration"].(map[string]any)
	servers, _ := config["iceServers"].([]any)
	var iceServers []webrtc.ICEServer
	for _, s := range servers {
		server, _ := s.(map[string]any)
		urls, _ := server["urls"].([]any)
		username, _ := server["username"].(string)
		credential, _ := server["credential"].(string)
		var urlStrs []string
		for _, u := range urls {
			if str, ok := u.(string); ok && str != "" {
				urlStrs = append(urlStrs, str)
			}
		}
		if len(urlStrs) > 0 {
			iceServers = append(iceServers, webrtc.ICEServer{
				URLs: urlStrs, Username: username, Credential: credential,
			})
		}
	}
	if len(iceServers) > 0 {
		newConf := webrtc.Configuration{ICEServers: iceServers}
		_ = p.pcSub.SetConfiguration(newConf)
		_ = p.pcPub.SetConfiguration(newConf)
	}
}

func (p *Peer) handleSubscriberOffer(payload map[string]any) {
	desc, _ := payload["description"].(map[string]any)
	sdp, _ := desc["sdp"].(string)
	_ = p.pcSub.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp})
	answer, _ := p.pcSub.CreateAnswer(nil)
	_ = p.pcSub.SetLocalDescription(answer)

	p.wsMu.Lock()
	_ = p.ws.WriteJSON(map[string]any{
		"roomId": p.roomInfo.RoomID, "event": "media-in", "groupId": p.groupID, "requestId": uuid.New().String(),
		"payload": map[string]any{"method": "rtc:answer", "description": map[string]any{"type": "answer", "sdp": answer.SDP}},
	})
	p.wsMu.Unlock()

	time.Sleep(300 * time.Millisecond)
	p.sendPublisherOffer()
}

func (p *Peer) sendPublisherOffer() {
	offer, _ := p.pcPub.CreateOffer(nil)
	_ = p.pcPub.SetLocalDescription(offer)
	p.wsMu.Lock()
	_ = p.ws.WriteJSON(map[string]any{
		"roomId": p.roomInfo.RoomID, "event": "media-in", "groupId": p.groupID, "requestId": uuid.New().String(),
		"payload": map[string]any{"method": "rtc:offer", "description": map[string]any{"type": "offer", "sdp": offer.SDP}},
	})
	p.wsMu.Unlock()
}

func (p *Peer) handlePublisherAnswer(payload map[string]any) {
	desc, _ := payload["description"].(map[string]any)
	sdp, _ := desc["sdp"].(string)
	_ = p.pcPub.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

func (p *Peer) handleICE(payload map[string]any) {
	candidates, _ := payload["rtcIceCandidates"].([]any)
	for _, c := range candidates {
		cand, _ := c.(map[string]any)
		candStr, _ := cand["candidate"].(string)
		target, _ := cand["target"].(string)
		sdpMid, _ := cand["sdpMid"].(string)
		index, _ := cand["sdpMLineIndex"].(float64)
		init := webrtc.ICECandidateInit{Candidate: candStr, SDPMid: &sdpMid, SDPMLineIndex: func() *uint16 { v := uint16(index); return &v }()}
		if target == "SUBSCRIBER" { _ = p.pcSub.AddICECandidate(init) } else { _ = p.pcPub.AddICECandidate(init) }
	}
}

func (p *Peer) Send(data []byte) error {
	if (p.dc == nil || p.dc.ReadyState() != webrtc.DataChannelStateOpen) && p.videoTrack == nil {
		return errors.New("no transport")
	}
	select {
	case p.sendQueue <- data: return nil
	case <-time.After(50 * time.Millisecond): return errors.New("timeout")
	}
}

func (p *Peer) processSendQueue() {
	for {
		select {
		case <-p.sessionCloseCh: return
		case <-p.closeCh: return
		case data := <-p.sendQueue:
			encoded := EncodeDataPacket(data)
			if p.videoTrack != nil {
				_ = p.videoTrack.WriteSample(media.Sample{Data: encoded, Duration: 10 * time.Millisecond})
			}
			if p.dc != nil && p.dc.ReadyState() == webrtc.DataChannelStateOpen {
				_ = p.dc.Send(encoded)
			}
			time.Sleep(sendDelay)
		}
	}
}

func (p *Peer) Close() error {
	p.closed.Store(true)
	close(p.closeCh)
	if p.dc != nil { p.dc.Close() }
	if p.pcPub != nil { p.pcPub.Close() }
	if p.pcSub != nil { p.pcSub.Close() }
	if p.ws != nil { p.wsMu.Lock(); _ = p.ws.Close(); p.wsMu.Unlock() }
	return nil
}

func (p *Peer) SetReconnectCallback(cb func(*webrtc.DataChannel)) { p.onReconnect = cb }
func (p *Peer) SetShouldReconnect(fn func() bool) { p.shouldReconnect = fn }
func (p *Peer) SetEndedCallback(cb func(string)) { p.onEnded = cb }
func (p *Peer) WatchConnection(ctx context.Context) {
	for { select { case <-ctx.Done(): return; case <-p.closeCh: return; case <-p.reconnectCh: } }
}
func (p *Peer) CanSend() bool { return len(p.sendQueue) < 4000 }
func (p *Peer) GetSendQueue() chan []byte { return p.sendQueue }
func (p *Peer) GetBufferedAmount() uint64 { if p.dc != nil { return p.dc.BufferedAmount() }; return 0 }
func (p *Peer) queueReconnect() {
	if p.closed.Load() || p.reconnecting.Load() { return }
	select { case p.reconnectCh <- struct{}{}: default: }
}
