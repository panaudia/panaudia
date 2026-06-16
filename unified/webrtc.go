package unified

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/pion/webrtc/v3"
)

type websocketMessage struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

type WebRTCConfig struct {
	ICEHost    string
	ICEPort    int
	Unticketed bool
}

type WebRTCServer struct {
	api          *webrtc.API
	webRtcConfig webrtc.Configuration
	upgrader     websocket.Upgrader
	config       WebRTCConfig
	backend      panaudia_server.Backend
	authoriser   panaudia_server.Authoriser
}

// NewWebRTCServer creates a pion WebRTC API with TCP-only ICE using the provided
// STUN listener (from the TCP demuxer) instead of opening a dedicated port.
func NewWebRTCServer(config WebRTCConfig, backend panaudia_server.Backend, authoriser panaudia_server.Authoriser, stunListener net.Listener) *WebRTCServer {
	s := &WebRTCServer{
		config:     config,
		backend:    backend,
		authoriser: authoriser,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	mediaEngine := &webrtc.MediaEngine{}
	settingEngine := &webrtc.SettingEngine{}

	// TCP-only ICE
	settingEngine.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeTCP4,
		webrtc.NetworkTypeTCP6,
	})

	// Optional: force ICE candidate address
	if config.ICEHost != "" {
		settingEngine.SetNAT1To1IPs([]string{config.ICEHost}, webrtc.ICECandidateTypeHost)
	}

	// Use the demuxer's STUN virtual listener for ICE TCP mux
	tcpMux := webrtc.NewICETCPMux(nil, stunListener, 8)
	settingEngine.SetICETCPMux(tcpMux)

	// Opus codec (48kHz stereo) — matches existing WebRTC server
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeOpus,
			ClockRate:    48000,
			Channels:     2,
			SDPFmtpLine:  "stereo=1; sprop-stereo=1; minptime=10; maxaveragebitrate=128000; useinbandfec=1",
			RTCPFeedback: nil,
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	s.api = webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithSettingEngine(*settingEngine),
	)

	s.webRtcConfig = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{
					"stun:stun.l.google.com:19302",
					"stun:stun.l.google.com:5349",
					"stun:stun.l.google.com:3478",
				},
			},
		},
	}

	return s
}

// JoinHandler handles WebSocket connections for WebRTC signaling.
func (s *WebRTCServer) JoinHandler(w http.ResponseWriter, r *http.Request) {
	unsafeConn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		common.LogError("WebRTC upgrade: %v", err)
		return
	}

	c := &threadSafeWriter{unsafeConn, sync.Mutex{}}

	var nodeConfig common.NodeConfig
	var configError error

	if s.config.Unticketed {
		nodeConfig, configError = s.authoriser.AuthoriseWithoutTicket(r.URL.Query())
	} else {
		nodeConfig, configError = s.authoriser.Authorise(r.URL.Query())
	}

	if configError != nil {
		sendErrorMessage(c, configError.Error())
		c.Close()
		return
	}

	s.handleConnection(c, nodeConfig)
}

func (s *WebRTCServer) handleConnection(c *threadSafeWriter, nodeConfig common.NodeConfig) {
	common.LogInfo("WebRTC connecting: %s - %s", nodeConfig.Uuid.String(), nodeConfig.Name)

	peerConnection, outputTrack := s.makeNewPeerConnection()
	connectionHandler := s.backend.NewConnectionHandler(nodeConfig, outputTrack)

	writer := panaudia_server.NewDataWriter(nodeConfig.Uuid, nodeConfig.SubSpaces, nodeConfig.ReadCaps)
	connectionHandler.SetReceiveSender(writer)

	peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		if i == nil {
			return
		}
		candidate := i.ToJSON()
		candidate.Candidate = rewriteCandidatePort(candidate.Candidate, s.config.ICEPort)
		candidateString, err := json.Marshal(candidate)
		if err != nil {
			common.LogError("ICECandidate marshal: %v", err)
			return
		}
		if writeErr := c.WriteJSON(&websocketMessage{
			Event: "candidate",
			Data:  string(candidateString),
		}); writeErr != nil {
			common.LogError("ICECandidate write: %v", writeErr)
		}
	})

	peerConnection.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		common.LogInfo("WebRTC stream started: %s - %s", nodeConfig.Uuid.String(), nodeConfig.Name)
		for connectionHandler.IsActive() {
			rtp, _, readErr := track.ReadRTP()
			if readErr != nil {
				common.LogDebug("WebRTC ReadRTP error: %v", readErr)
				continue
			}
			if len(rtp.Payload) != 0 {
				if err := connectionHandler.WriteOpus(rtp.Payload); err != nil {
					common.LogError("WebRTC WriteOpus: %v", err)
				}
			}
		}
	})

	s.addDataChannels(peerConnection, nodeConfig, writer, connectionHandler)

	defer func() {
		if cErr := peerConnection.Close(); cErr != nil {
			common.LogDebug("WebRTC peerConnection close: %v", cErr)
		}
		if connectionHandler != nil {
			connectionHandler.Stop()
		}
		c.Close()
		common.LogInfo("WebRTC disconnected: %s - %s", nodeConfig.Uuid.String(), nodeConfig.Name)
	}()

	// Send SDP offer
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		common.LogError("CreateOffer: %v", err)
		return
	}
	if err = peerConnection.SetLocalDescription(offer); err != nil {
		common.LogError("SetLocalDescription: %v", err)
		return
	}
	offerToSend := offer
	offerToSend.SDP = rewriteSDPCandidatePorts(offerToSend.SDP, s.config.ICEPort)
	offerString, err := json.Marshal(offerToSend)
	if err != nil {
		common.LogError("Marshal offer: %v", err)
		return
	}
	if err = c.WriteJSON(&websocketMessage{
		Event: "offer",
		Data:  string(offerString),
	}); err != nil {
		common.LogError("WriteJSON offer: %v", err)
		return
	}

	go func() {
		connectError := connectionHandler.Connect()
		if connectError != nil {
			sendErrorMessage(c, connectError.Message)
		}
	}()

	// Listen for WebSocket messages (SDP answer + ICE candidates)
	s.listenToWebsocket(c, peerConnection, connectionHandler)
}

func (s *WebRTCServer) makeNewPeerConnection() (*webrtc.PeerConnection, *webrtc.TrackLocalStaticSample) {
	peerConnection, err := s.api.NewPeerConnection(s.webRtcConfig)
	if err != nil {
		panic(err)
	}

	outputTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType:     webrtc.MimeTypeOpus,
		ClockRate:    48000,
		Channels:     2,
		SDPFmtpLine:  "stereo=1; sprop-stereo=1; minptime=10; maxaveragebitrate=48000; useinbandfec=1",
		RTCPFeedback: nil,
	}, "audio", "panaudia")
	if err != nil {
		panic(err)
	}

	rtpSender, err := peerConnection.AddTrack(outputTrack)
	if err != nil {
		panic(err)
	}

	// Drain RTCP
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	return peerConnection, outputTrack
}

func (s *WebRTCServer) addDataChannels(peerConnection *webrtc.PeerConnection, nodeConfig common.NodeConfig, writer *panaudia_server.DataWriter, connectionHandler panaudia_server.ConnectionHandler) {
	// State channel: position/rotation updates
	stateChannel, err := peerConnection.CreateDataChannel("state", nil)
	if err != nil {
		panic(err)
	}
	if nodeConfig.ReturnData {
		stateChannel.OnOpen(func() {
			writer.StateDataChannel = stateChannel
		})
	}
	stateChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		nodeInfo := inout.NodeInfo3FromBytes(msg.Data)
		connectionHandler.SetRotation(nodeInfo.Rotation)
		connectionHandler.SetPosition(nodeInfo.Position)
	})

	// Attributes channel: source info refresh
	attributesChannel, err := peerConnection.CreateDataChannel("attributes", nil)
	if err != nil {
		panic(err)
	}
	attributesChannel.OnOpen(func() {
		writer.SourceDataChannel = attributesChannel
	})

	// Control channel: mute/unmute and other commands
	controlChannel, err := peerConnection.CreateDataChannel("control", nil)
	if err != nil {
		panic(err)
	}
	controlChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		message := common.ControlMessage{}
		if err := json.Unmarshal(msg.Data, &message); err != nil {
			common.LogError("Control unmarshal: %v", err)
			return
		}
		message.NodeId = nodeConfig.Uuid.String()
		connectionHandler.ControlMessage(message)
	})
}

func (s *WebRTCServer) listenToWebsocket(c *threadSafeWriter, peerConnection *webrtc.PeerConnection, connectionHandler panaudia_server.ConnectionHandler) {
	message := &websocketMessage{}

	for connectionHandler.IsActive() {
		_, raw, err := c.ReadMessage()
		if err != nil {
			connectionHandler.Stop()
			common.LogDebug("WebRTC websocket read: %v", err)
			return
		}
		if err := json.Unmarshal(raw, message); err != nil {
			common.LogError("WebRTC websocket unmarshal: %v", err)
			return
		}

		switch message.Event {
		case "candidate":
			candidate := webrtc.ICECandidateInit{}
			if err := json.Unmarshal([]byte(message.Data), &candidate); err != nil {
				common.LogError("ICECandidate unmarshal: %v", err)
				return
			}
			if err := peerConnection.AddICECandidate(candidate); err != nil {
				common.LogError("AddICECandidate: %v", err)
				return
			}

		case "answer":
			answer := webrtc.SessionDescription{}
			if err := json.Unmarshal([]byte(message.Data), &answer); err != nil {
				common.LogError("SDP answer unmarshal: %v", err)
				return
			}
			if err := peerConnection.SetRemoteDescription(answer); err != nil {
				common.LogError("SetRemoteDescription: %v", err)
				return
			}
		}
	}
}

// threadSafeWriter wraps gorilla/websocket for concurrent writes.
type threadSafeWriter struct {
	*websocket.Conn
	sync.Mutex
}

func (t *threadSafeWriter) WriteJSON(v interface{}) error {
	t.Lock()
	defer t.Unlock()
	return t.Conn.WriteJSON(v)
}

func sendErrorMessage(c *threadSafeWriter, message string) {
	errorInfo := map[string]string{"message": message}
	errorString, err := json.Marshal(errorInfo)
	if err != nil {
		common.LogError("sendErrorMessage marshal: %v", err)
		return
	}
	if err = c.WriteJSON(&websocketMessage{
		Event: "error",
		Data:  string(errorString),
	}); err != nil {
		common.LogError("sendErrorMessage write: %v", err)
	}
}

// rewriteSDPCandidatePorts rewrites the port of every host ICE candidate in
// an SDP blob to icePort. Used when the server sits behind a port-translating
// proxy (e.g. an LB terminating 443 and forwarding to PANAUDIA_PORT) so the
// candidate advertises the public-facing port. A non-positive icePort is a
// no-op (the natural listening port is already correct — the standalone case).
func rewriteSDPCandidatePorts(sdp string, icePort int) string {
	if icePort <= 0 {
		return sdp
	}

	lines := strings.Split(sdp, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "a=candidate:") {
			continue
		}
		lines[i] = rewriteSDPLineCandidatePort(line, icePort)
	}
	return strings.Join(lines, "\n")
}

func rewriteSDPLineCandidatePort(line string, icePort int) string {
	trimmed := strings.TrimSpace(line)
	updated := rewriteCandidatePort(strings.TrimPrefix(trimmed, "a="), icePort)
	if updated == strings.TrimPrefix(trimmed, "a=") {
		return line
	}
	return fmt.Sprintf("a=%s", updated)
}

func rewriteCandidatePort(candidate string, icePort int) string {
	if icePort <= 0 || candidate == "" {
		return candidate
	}

	fields := strings.Fields(candidate)
	if len(fields) < 8 {
		return candidate
	}
	if !strings.HasPrefix(fields[0], "candidate:") {
		return candidate
	}

	// Candidate format: "candidate:<foundation> <component> <transport> <priority> <ip> <port> typ <type> ..."
	if fields[6] != "typ" || fields[7] != "host" {
		return candidate
	}
	if _, err := strconv.Atoi(fields[5]); err != nil {
		return candidate
	}

	fields[5] = strconv.Itoa(icePort)
	return strings.Join(fields, " ")
}
