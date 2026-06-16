package panaudia_server

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/panaudia/panaudia/core/common"
)

// RocSignaling carries the ROC (Panaudia Link) websocket signalling
// handlers, decoupled from PanaudiaServer's WebRTC machinery. It needs
// only a backend (to mint ROC connection handlers) and an authoriser (to
// resolve ROC in/out config from the query string). ROC media flows over
// its own negotiated RTP/UDP ports — nothing here touches the HTTP/QUIC
// listener — so these handlers can be registered onto any mux (the legacy
// PanaudiaServer mux, or the unified single-port server).
//
// ROC carries no explicit auth (cloud wraps it in WireGuard; spatial is
// query-param only), so there is no ticket/secret check on this path.
type RocSignaling struct {
	upgrader   websocket.Upgrader
	backend    Backend
	authoriser Authoriser
}

func NewRocSignaling(backend Backend, authoriser Authoriser) *RocSignaling {
	return &RocSignaling{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		backend:    backend,
		authoriser: authoriser,
	}
}

// RegisterRoutes wires the ROC signalling endpoints onto the given mux.
// Callers that need to gate input vs output independently can register
// RocInHandler / RocOutHandler directly instead.
func (rs *RocSignaling) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/roc-input", rs.RocInHandler)
	mux.HandleFunc("/roc-output", rs.RocOutHandler)
}

// --- ROC input (Panaudia Link in) ---

func (rs *RocSignaling) RocInHandler(w http.ResponseWriter, r *http.Request) {

	unsafeConn, err := rs.upgrader.Upgrade(w, r, nil)

	if err != nil {
		common.LogError("upgrade:: %v/n", err)
		return
	}

	c := &threadSafeWriter{unsafeConn, sync.Mutex{}}

	config, configError := rs.authoriser.GetRocInConfig(r.URL.Query())

	if configError != nil {
		c.sendErrorMessage(configError.Error())
		err := c.Close()
		if err != nil {
			return
		}
		return
	}

	trackCount, err3 := common.Uint32FromQuery("tracks", r.URL.Query())

	if err3 != nil {
		c.sendErrorMessage("query value tracks is missing")
		err := c.Close()
		if err != nil {
			return
		}
		return
	}

	rs.rocWebsocketHandler(c, w, r, config, trackCount)
}

func (rs *RocSignaling) rocWebsocketHandler(c *threadSafeWriter, w http.ResponseWriter, r *http.Request, config common.RocInConnectConfig, trackCount uint32) {

	common.LogInfo("Connecting Link Input - %d tracks", trackCount)

	rocConnectionHandler := rs.backend.NewRocConnectionHandler(trackCount)

	go func() {
		rs.listenToRocWebsocket(c, rocConnectionHandler, config)
	}()

	rs.sendRocPorts(c, rocConnectionHandler, config)

	common.LogDebug("Sent ports")

	rocConnectionHandler.Connect()

	common.LogInfo("Disconnecting Link Input - %d tracks", trackCount)

}

func (rs *RocSignaling) sendRocPorts(c *threadSafeWriter, connectionHandler RocConnectionHandler, config common.RocInConnectConfig) {

	ports := connectionHandler.Ports()

	portsString, err := json.Marshal(ports)
	if err != nil {
		common.LogError("Error in Marshal offer: %v", err)
		return
	}

	if err = c.WriteJSON(&websocketMessage{
		Event: "ports",
		Data:  string(portsString),
	}); err != nil {
		common.LogError("Error in WriteJSON for offer: %v", err)
		return
	}
}

func (rs *RocSignaling) listenToRocWebsocket(c *threadSafeWriter, connectionHandler RocConnectionHandler, config common.RocInConnectConfig) {

	message := &websocketMessage{}

	for connectionHandler.IsActive() {
		_, raw, err := c.ReadMessage()
		if err != nil {
			connectionHandler.Stop()
			common.LogDebug("Error in ReadMessage %v", err)
			return
		} else if err := json.Unmarshal(raw, &message); err != nil {
			common.LogError("Error in Unmarshal %v", err)
			return
		}

		switch message.Event {

		case "config":
			inputConfig := common.RocInputConfig{}
			if err := json.Unmarshal([]byte(message.Data), &inputConfig); err != nil {
				common.LogError("Error in Unmarshal SessionDescription: %v", err)
				return
			}

			rocConfig, configError := common.RocConfigFromRocInputConfig(inputConfig, config.MixerHost, config.BouncerHost)

			if configError != nil {
				connectionHandler.Stop()
				common.LogDebug("Error in RocConfigFromRocInputConfig %v", err)
			} else {
				connectionHandler.Configure(rocConfig)
				common.LogDebug("got config")
			}

		case "position":
			positionInfo := common.MultiTrackPositionInfo{}
			if err := json.Unmarshal([]byte(message.Data), &positionInfo); err != nil {
				common.LogError("Error in Unmarshal positionInfo: %v", err)
				return
			}
			common.LogDebug("positionInfo %v", positionInfo)
			connectionHandler.SetPosition(positionInfo.Track, positionInfo.Position)
			connectionHandler.SetRotation(positionInfo.Track, positionInfo.Rotation)
		}
	}
}

// --- ROC output (Panaudia Link ambisonic out) ---

func (rs *RocSignaling) RocOutHandler(w http.ResponseWriter, r *http.Request) {

	unsafeConn, err := rs.upgrader.Upgrade(w, r, nil)

	common.LogDebug("rocOutHandler")

	if err != nil {
		common.LogError("upgrade:: %v/n", err)
		return
	}

	c := &threadSafeWriter{unsafeConn, sync.Mutex{}}

	rocOutConfig, configError := rs.authoriser.GetRocOutConfig(r.URL.Query())

	if configError != nil {
		c.sendErrorMessage(configError.Error())
		common.LogDebug("rocOutHandler:: %v/n", configError)
		err := c.Close()
		if err != nil {
			return
		}
		return
	}

	rs.rocOutWebsocketHandler(c, w, r, rocOutConfig)
}

func (rs *RocSignaling) rocOutWebsocketHandler(c *threadSafeWriter, w http.ResponseWriter, r *http.Request, rocOutConfig common.RocOutputConfig) {

	common.LogInfo("Connecting Link Ambisonic ReverbOutput")
	rocOutConnectionHandler := rs.backend.NewRocOutConnectionHandler(rocOutConfig)

	rocOutConnectionHandler.Connect()

	rs.sendConnected(c)

	rs.listenToRocOutWebsocket(c, rocOutConnectionHandler)

	common.LogInfo("Disconnecting Roc out websocket")
}

func (rs *RocSignaling) sendConnected(c *threadSafeWriter) {

	errorInfo := map[string]string{"message": "connected"}

	errorString, err := json.Marshal(errorInfo)
	if err != nil {
		common.LogError("sendErrorMessage error: %v", err)
		return
	}

	if err := c.WriteJSON(&websocketMessage{
		Event: "connected",
		Data:  string(errorString),
	}); err != nil {
		common.LogError("Error in WriteJSON for offer: %v", err)
		return
	}
}

func (rs *RocSignaling) listenToRocOutWebsocket(c *threadSafeWriter, connectionHandler RocOutConnectionHandler) {

	message := &websocketMessage{}

	for true {
		_, raw, err := c.ReadMessage()
		if err != nil {
			connectionHandler.Stop()
			common.LogDebug("Error in ReadMessage %v", err)
			return
		} else if err := json.Unmarshal(raw, &message); err != nil {
			common.LogError("Error in Unmarshal %v", err)
			return
		}

		switch message.Event {

		case "position":
			positionInfo := common.MultiTrackPositionInfo{}
			if err := json.Unmarshal([]byte(message.Data), &positionInfo); err != nil {
				common.LogError("Error in Unmarshal positionInfo: %v", err)
				return
			}
			common.LogDebug("positionInfo %v", positionInfo)
			connectionHandler.SetPosition(positionInfo.Track, positionInfo.Position)
			//connectionHandler.SetRotation(positionInfo.Track, positionInfo.Rotation)

		case "disconnect":
			common.LogDebug("output disconnect msg")
			connectionHandler.Stop()
			return
		}

	}
}
