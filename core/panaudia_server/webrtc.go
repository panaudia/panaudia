package panaudia_server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/panaudia/panaudia/core/common"
	octspace_io "github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/pion/webrtc/v3"
)

// WS liveness probing for the WebRTC signalling connection. The ping
// keeps a healthy connection's read deadline extended (browsers pong
// automatically); a half-open TCP connection stops ponging and the
// deadline unblocks ReadMessage within wsReadDeadline.
const (
	wsPingInterval = 15 * time.Second
	wsReadDeadline = 45 * time.Second
)

func (server *PanaudiaServer) joinHandler(w http.ResponseWriter, r *http.Request) {

	unsafeConn, err := server.upgrader.Upgrade(w, r, nil)

	if err != nil {
		common.LogError("upgrade:: %v/n", err)
		return
	}
	// 	fmt.Printf("joinHandler 2\n")

	c := &threadSafeWriter{unsafeConn, sync.Mutex{}}

	var nodeConfig common.NodeConfig
	var configError error

	if server.config.Unticketed {
		nodeConfig, configError = server.authoriser.AuthoriseWithoutTicket(r.URL.Query())
	} else {
		nodeConfig, configError = server.authoriser.Authorise(r.URL.Query())
	}

	if configError != nil {
		c.sendErrorMessage(configError.Error())
		err := c.Close()
		if err != nil {
			return
		}
		return
	}

	server.websocketHandler(c, w, r, nodeConfig)
}

func (server *PanaudiaServer) websocketHandler(c *threadSafeWriter, w http.ResponseWriter, r *http.Request, nodeConfig common.NodeConfig) {

	common.LogInfo("Connecting:     %s - %s", nodeConfig.Uuid.String(), nodeConfig.Name)
	peerConnection, outputTrack := server.makeNewPeerConnection()

	// The transport's session handle: Alive while the PeerConnection
	// hasn't failed/closed; Kill severs both the WebSocket and the
	// PeerConnection, which unblocks the read loop below — this
	// goroutine is the session's owner and runs the departure
	// (connectionHandler.Stop in the defer chain) as it exits.
	live := &sessions.FuncSession{
		AliveFn: func() bool {
			state := peerConnection.ConnectionState()
			return state != webrtc.PeerConnectionStateClosed &&
				state != webrtc.PeerConnectionStateFailed
		},
		KillFn: func(reason string) {
			common.LogInfo("Killing WebRTC session %s: %s", nodeConfig.Uuid, reason)
			_ = peerConnection.Close()
			_ = c.Close()
		},
	}

	connectionHandler, serr := NewConnectionHandlerE(server.backend, nodeConfig, outputTrack, live, "webrtc")
	if connectionHandler == nil {
		// Duplicate identity / server full / internal error — reject the
		// join explicitly rather than half-admitting (a nil handler here
		// previously panicked the http handler).
		common.LogWarn("Rejecting connection for %s: %v", nodeConfig.Uuid, serr)
		if serr != nil {
			c.sendErrorMessage(serr.Message)
		} else {
			c.sendErrorMessage("connection failed")
		}
		if err := peerConnection.Close(); err != nil {
			common.LogDebug("peerConnection close after rejected join: %v", err)
		}
		_ = c.Close()
		return
	}

	// Funnel (mechanism-design §3a): pion state changes never perform
	// cleanup themselves — failed/closed is just a Kill cause; the
	// severed transport unblocks the owner goroutine, which departs.
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			live.Kill("peerconnection " + state.String())
		}
	})

	writer := NewDataWriter(nodeConfig.Uuid, nodeConfig.SubSpaces, nodeConfig.ReadCaps)
	connectionHandler.SetReceiveSender(writer)

	// set callbacks
	peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		server.onICECandidate(i, c)
	})

	peerConnection.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		server.onTrack(track, c, connectionHandler, nodeConfig)
	})

	server.addDataChannels(peerConnection, nodeConfig, writer, connectionHandler)

	defer func() {
		if cErr := peerConnection.Close(); cErr != nil {
			common.LogDebug("cannot close peerConnection: %v", cErr)
		}
		if connectionHandler != nil {
			connectionHandler.Stop()
		}
		c.Close()
		common.LogInfo("Disconnecting:  %s - %s", nodeConfig.Uuid.String(), nodeConfig.Name)
	}()

	server.sendOffer(c, peerConnection)

	go func() {
		connectError := connectionHandler.Connect()
		if connectError != nil {
			c.sendErrorMessage(connectError.Message)
		}

	}()

	// WS ping + read deadline: a half-open TCP connection must not be
	// able to block ReadMessage forever — the deadline expiry errors the
	// read, the owner goroutine exits, and the departure runs. Browsers
	// answer pings automatically; each pong extends the deadline.
	_ = c.SetReadDeadline(time.Now().Add(wsReadDeadline))
	c.SetPongHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(wsReadDeadline))
	})
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ticker.C:
				if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			}
		}
	}()

	server.listenToWebsocket(c, peerConnection, connectionHandler)

}

func (server *PanaudiaServer) makeNewPeerConnection() (*webrtc.PeerConnection, *webrtc.TrackLocalStaticSample) {

	// Create a new RTCPeerConnection
	peerConnection, err := server.api.NewPeerConnection(server.webRtcConfig)
	if err != nil {
		panic(err)
	}

	outputTrack := server.addOutputTrack(peerConnection)

	return peerConnection, outputTrack
}

func (server *PanaudiaServer) addOutputTrack(peerConnection *webrtc.PeerConnection) *webrtc.TrackLocalStaticSample {
	// Create Track that we send audio back to browser on
	outputTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "stereo=1; sprop-stereo=1; minptime=10; maxaveragebitrate=48000; useinbandfec=1", RTCPFeedback: nil}, "audio", "panaudia")
	if err != nil {
		panic(err)
	}

	// Add this newly Created track to the PeerConnection
	rtpSender, err := peerConnection.AddTrack(outputTrack)
	if err != nil {
		panic(err)
	}

	// Read incoming RTCP packets
	// Before these packets are returned they are processed by interceptors. For things
	// like NACK this needs to be called.
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	return outputTrack
}

func (server *PanaudiaServer) addDataChannels(peerConnection *webrtc.PeerConnection, nodeConfig common.NodeConfig, writer *DataWriter, connectionHandler ConnectionHandler) {
	stateChannel, err := peerConnection.CreateDataChannel("state", nil)
	if err != nil {
		panic(err)
	}

	server.initStateChannel(stateChannel, nodeConfig, writer, connectionHandler)

	attributesChannel, err := peerConnection.CreateDataChannel("attributes", nil)
	if err != nil {
		panic(err)
	}
	server.initAttributesChannel(attributesChannel, writer, connectionHandler)

	entityChannel, err := peerConnection.CreateDataChannel("entity", nil)
	if err != nil {
		panic(err)
	}
	server.initEntityChannel(entityChannel, writer, connectionHandler)

	// Space channel is always created (read-cap-blind). DataWriter's
	// sendSpace path gates on the holder's commands.ReadCapSpaceRead
	// at send time — for unauthorised holders the channel just stays
	// idle. Keeping creation unconditional avoids splitting the SDP
	// offer per ticket-claim. See plan/history/commands/space-read-path-plan.md.
	spaceChannel, err := peerConnection.CreateDataChannel("space", nil)
	if err != nil {
		panic(err)
	}
	server.initSpaceChannel(spaceChannel, writer, connectionHandler)

	controlChannel, err := peerConnection.CreateDataChannel("control", nil)
	if err != nil {
		panic(err)
	}
	server.initControlChannel(controlChannel, nodeConfig, connectionHandler)
}

func (server *PanaudiaServer) onICECandidate(i *webrtc.ICECandidate, c *threadSafeWriter) {
	if i == nil {
		return
	}

	candidateString, err := json.Marshal(i.ToJSON())
	if err != nil {
		common.LogError("onICECandidate marshalling ICECandidate: %v", err)
		return
	}

	if writeErr := c.WriteJSON(&websocketMessage{
		Event: "candidate",
		Data:  string(candidateString),
	}); writeErr != nil {
		common.LogError("onICECandidate marshalling ICECandidate: %v", writeErr)
	}
}

func (server *PanaudiaServer) onTrack(track *webrtc.TrackRemote, c *threadSafeWriter, connectionHandler ConnectionHandler, nodeConfig common.NodeConfig) {
	common.LogInfo("Stream started: %s - %s", nodeConfig.Uuid.String(), nodeConfig.Name)
	common.LogDebug("Track  type %d: %s", track.PayloadType(), track.Codec().MimeType)

	for connectionHandler.IsActive() {
		// Read RTP packets being sent to Pion
		rtp, _, readErr := track.ReadRTP()
		if readErr != nil {
			// A read error here is terminal (track/transport closed) —
			// exit instead of spinning on a dead track. The departure
			// itself is the websocket owner goroutine's job.
			common.LogDebug("RTP read loop exiting for %s: %v", nodeConfig.Uuid, readErr)
			return
		}
		opusDataIn := rtp.Payload

		//fmt.Printf("opusDataIn %v \n", opusDataIn)

		if len(opusDataIn) != 0 {
			// A WriteOpus error is a decode/buffer problem for ONE frame
			// — log and continue; transport loss surfaces as a ReadRTP
			// error above, and session teardown is owned by the
			// websocket owner goroutine (the funnel), not this loop.
			if err2 := connectionHandler.WriteOpus(opusDataIn); err2 != nil {
				common.LogError("Write failed in WriteOpus: %v", err2)
			}
		} else {
			common.LogError("no opus Data")
		}
	}
}

func (server *PanaudiaServer) initControlChannel(d *webrtc.DataChannel, nodeConfig common.NodeConfig, connectionHandler ConnectionHandler) {

	d.OnMessage(func(msg webrtc.DataChannelMessage) {

		message := common.ControlMessage{}
		if err := json.Unmarshal([]byte(msg.Data), &message); err != nil {
			common.LogError("Error in Unmarshal SessionDescription: %v", err)
			return
		}
		message.NodeId = nodeConfig.Uuid.String()
		connectionHandler.ControlMessage(message)

	})
}

func (server *PanaudiaServer) initStateChannel(d *webrtc.DataChannel, nodeConfig common.NodeConfig, writer *DataWriter, connectionHandler ConnectionHandler) {
	// Register channel opening handling
	if nodeConfig.ReturnData {
		d.OnOpen(func() {
			writer.StateDataChannel = d
		})
	}

	d.OnMessage(func(msg webrtc.DataChannelMessage) {
		nodeInfo := octspace_io.NodeInfo3FromBytes(msg.Data)
		connectionHandler.SetRotation(nodeInfo.Rotation)
		connectionHandler.SetPosition(nodeInfo.Position)
	})
}

func (server *PanaudiaServer) initAttributesChannel(d *webrtc.DataChannel, writer *DataWriter, connectionHandler ConnectionHandler) {
	d.OnOpen(func() {
		// Attach + flush any envelopes that arrived during the SCTP
		// handshake (the cache backfill triggered on SetReceiveSender
		// races channel open).
		writer.AttachAttributesChannel(d)
	})
}

func (server *PanaudiaServer) initEntityChannel(d *webrtc.DataChannel, writer *DataWriter, connectionHandler ConnectionHandler) {
	d.OnOpen(func() {
		writer.AttachEntityChannel(d)
	})
}

func (server *PanaudiaServer) initSpaceChannel(d *webrtc.DataChannel, writer *DataWriter, connectionHandler ConnectionHandler) {
	d.OnOpen(func() {
		// Drain any envelopes that arrived while the SCTP handshake
		// was in flight. For holders without commands.ReadCapSpaceRead
		// the pending queue is always empty (sendSpace early-returns
		// before buffering), so this is a no-op for them.
		writer.AttachSpaceChannel(d)
	})
}

func (server *PanaudiaServer) sendOffer(c *threadSafeWriter, peerConnection *webrtc.PeerConnection) {

	// Create an offer
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		common.LogError("Error in CreateOffer: %v", err)
		return
	}

	if err = peerConnection.SetLocalDescription(offer); err != nil {
		common.LogError("Error in SetLocalDescription: %v", err)
		return
	}

	common.LogDebug("Sending offer to peer: %v", offer)

	offerString, err := json.Marshal(offer)
	if err != nil {

		common.LogError("Error in Marshal offer: %v", err)
		return
	}

	if err = c.WriteJSON(&websocketMessage{
		Event: "offer",
		Data:  string(offerString),
	}); err != nil {
		common.LogError("Error in WriteJSON for offer: %v", err)
		return
	}
}

func (server *PanaudiaServer) listenToWebsocket(c *threadSafeWriter, peerConnection *webrtc.PeerConnection, connectionHandler ConnectionHandler) {

	message := &websocketMessage{}

	for connectionHandler.IsActive() {
		_, raw, err := c.ReadMessage()
		if err != nil {
			// Owner-goroutine exit: the departure runs once, in the
			// websocketHandler defer chain (connectionHandler.Stop).
			common.LogDebug("Error in ReadMessage %v", err)
			return
		} else if err := json.Unmarshal(raw, &message); err != nil {
			common.LogError("Error in Unmarshal %v", err)
			return
		}

		switch message.Event {

		case "candidate":
			candidate := webrtc.ICECandidateInit{}
			if err := json.Unmarshal([]byte(message.Data), &candidate); err != nil {
				common.LogError("Error in Unmarshal %v", err)
				return
			}

			if err := peerConnection.AddICECandidate(candidate); err != nil {
				common.LogError("Error in AddICECandidate %v", err)
				return
			}

		case "answer":
			answer := webrtc.SessionDescription{}
			if err := json.Unmarshal([]byte(message.Data), &answer); err != nil {
				common.LogError("Error in Unmarshal SessionDescription: %v", err)
				return
			}

			if err := peerConnection.SetRemoteDescription(answer); err != nil {
				common.LogError("Error in SetRemoteDescription: %v", err)
				return
			}
		}
	}
}
