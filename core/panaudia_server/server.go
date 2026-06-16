package panaudia_server

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/panaudia/panaudia/core/common"
	"github.com/pion/webrtc/v3"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
)

type websocketMessage struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

type PanaudiaServerConfig struct {
	Host         string
	ICEHost      string
	Port         int
	RTCPort      int
	TLSCrt       string
	TLSKey       string
	Unticketed   bool
	EnableWebRTC bool
	EnableROCIn  bool
	EnableROCOut bool
}

type PanaudiaServer struct {
	api           *webrtc.API
	mediaEngine   *webrtc.MediaEngine
	settingEngine *webrtc.SettingEngine
	webRtcConfig  webrtc.Configuration
	upgrader      websocket.Upgrader
	config        PanaudiaServerConfig
	backend       Backend
	authoriser    Authoriser
	roc           *RocSignaling
}

func NewPanaudiaServer(config PanaudiaServerConfig, backend Backend, authoriser Authoriser) *PanaudiaServer {

	server := PanaudiaServer{config: config,
		backend:    backend,
		authoriser: authoriser,
		roc:        NewRocSignaling(backend, authoriser)}

	server.upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	server.mediaEngine = &webrtc.MediaEngine{}
	server.settingEngine = &webrtc.SettingEngine{}

	//////////////////

	// Configure our SettingEngine to use our UDPMux. By default a PeerConnection has
	// no global state. The API+SettingEngine allows the user to share state between them.
	// In this case we are sharing our listening port across many.
	// Listen on UDP Port 8443, will be used for all WebRTC traffic
	//mux, err := ice.NewMultiUDPMuxFromPort(config.RTCPort)
	//if err != nil {
	//	panic(err)
	//}
	//fmt.Printf("Listening for WebRTC traffic at %d\n", config.RTCPort)

	/////

	// Enable support only for TCP ICE candidates.
	server.settingEngine.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeTCP4,
		webrtc.NetworkTypeTCP6,
	})

	// if the IceHost is given this will force ICE to give it as a response rather than look one up using STUN
	if config.ICEHost != "" {
		externalAddresses := []string{config.ICEHost}
		server.settingEngine.SetNAT1To1IPs(externalAddresses, webrtc.ICECandidateTypeHost)
	}

	tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IP{0, 0, 0, 0},
		Port: config.RTCPort,
	})
	if err != nil {
		panic(err)
	}

	//fmt.Printf("Listening for ICE TCP at %s\n", tcpListener.Addr())

	tcpMux := webrtc.NewICETCPMux(nil, tcpListener, 8)
	server.settingEngine.SetICETCPMux(tcpMux)

	///////

	//panaudia_server.settingEngine.SetICEUDPMux(mux)

	// Create a new API using our SettingEngine

	//// Create a InterceptorRegistry. This is the user configurable RTP/RTCP Pipeline.
	//// This provides NACKs, RTCP Reports and other features. If you use `webrtc.NewPeerConnection`
	//// this is enabled by default. If you are manually managing You MUST create a InterceptorRegistry
	//// for each PeerConnection.
	//i := &interceptor.Registry{}
	//
	//// Use the default set of Interceptors
	//if err := webrtc.RegisterDefaultInterceptors(panaudia_server.mediaEngine, i); err != nil {
	//	panic(err)
	//}

	// Create the API object with the MediaEngine
	server.api = webrtc.NewAPI(webrtc.WithMediaEngine(server.mediaEngine), webrtc.WithSettingEngine(*server.settingEngine))

	/////////////////

	// Setup the codecs you want to use.
	if err := server.mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "stereo=1; sprop-stereo=1; minptime=10; maxaveragebitrate=128000; useinbandfec=1", RTCPFeedback: nil},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	server.webRtcConfig = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302",
					"stun:stun.l.google.com:5349",
					"stun:stun.l.google.com:3478",
				},
			},
		},
	}
	return &server
}

func logFileContents(path string) {

	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() {
		if err = file.Close(); err != nil {
			log.Fatal(err)
		}
	}()

	b, err := io.ReadAll(file)
	common.LogDebug("file: %v", string(b))
}

func (server *PanaudiaServer) Serve() {

	http.HandleFunc("/probe", server.probeHandler)
	http.HandleFunc("/hello", server.helloHandler)
	if server.config.EnableWebRTC {
		http.HandleFunc("/join", server.joinHandler)
	}

	if server.config.EnableROCIn {
		http.HandleFunc("/roc-input", server.roc.RocInHandler)
	}

	if server.config.EnableROCOut {
		http.HandleFunc("/roc-output", server.roc.RocOutHandler)
	}

	address := fmt.Sprintf("%s:%d", server.config.Host, server.config.Port)

	if server.config.TLSCrt == "" {
		log.Fatal(http.ListenAndServe(address, nil))
	} else {
		if server.config.TLSCrt != "" {
			common.LogDebug("Using TLS")
			//common.LogDebug("panaudia_server crt:")
			logFileContents(server.config.TLSCrt)
			//common.LogDebug("panaudia_server key:")
			logFileContents(server.config.TLSKey)
		}
		log.Fatal(http.ListenAndServeTLS(address, server.config.TLSCrt, server.config.TLSKey, nil))
	}
}

func (server *PanaudiaServer) ServeNoROC() {

	http.HandleFunc("/probe", server.probeHandler)
	http.HandleFunc("/hello", server.helloHandler)
	if server.config.EnableWebRTC {
		http.HandleFunc("/join", server.joinHandler)
	}

	address := fmt.Sprintf("%s:%d", server.config.Host, server.config.Port)

	if server.config.TLSCrt == "" {
		log.Fatal(http.ListenAndServe(address, nil))
	} else {
		if server.config.TLSCrt != "" {
			common.LogDebug("Using TLS")
			//common.LogDebug("panaudia_server crt:")
			logFileContents(server.config.TLSCrt)
			//common.LogDebug("panaudia_server key:")
			logFileContents(server.config.TLSKey)
		}
		log.Fatal(http.ListenAndServeTLS(address, server.config.TLSCrt, server.config.TLSKey, nil))
	}
}

func (server *PanaudiaServer) viewHandler(w http.ResponseWriter, r *http.Request) {
	unsafeConn, err := server.upgrader.Upgrade(w, r, nil)

	if err != nil {
		common.LogError("upgrade:: %v/n", err)
		return
	}

	c := &threadSafeWriter{unsafeConn, sync.Mutex{}}
	defer func() {
		c.Close()
	}()

	message := &websocketMessage{}
	for {
		_, raw, err := c.ReadMessage()
		if err != nil {
			log.Println(err)
			return
		} else if err := json.Unmarshal(raw, &message); err != nil {
			log.Println(err)
			return
		}

		switch message.Event {

		case "ping":
			common.LogDebug("ping")
			if err = c.WriteJSON(&websocketMessage{
				Event: "answer",
				Data:  "pong",
			}); err != nil {
				log.Println(err)
				return
			}
		}
	}
}

func (server *PanaudiaServer) helloHandler(w http.ResponseWriter, r *http.Request) {
	_, err := io.WriteString(w, "Hello from Panaudia\n")
	if err != nil {
		return
	}
}

func (server *PanaudiaServer) probeHandler(w http.ResponseWriter, r *http.Request) {
	_, err := io.WriteString(w, "ok\n")
	if err != nil {
		return
	}
}

//func renderJson(w http.ResponseWriter, jsonObject common.J) error {
//	answerString, err := json.Marshal(jsonObject)
//	if err != nil {
//		return err
//	}
//
//	fmt.Fprintf(w, string(answerString[:]))
//	return nil
//}

// Helper to make Gorilla Websockets threadsafe
type threadSafeWriter struct {
	*websocket.Conn
	sync.Mutex
}

func (t *threadSafeWriter) sendErrorMessage(message string) {
	errorInfo := map[string]string{"message": message}

	common.LogInfo("sending error message: %v", message)

	errorString, err := json.Marshal(errorInfo)
	if err != nil {
		common.LogError("sendErrorMessage error: %v", err)
		return
	}

	if err = t.WriteJSON(&websocketMessage{
		Event: "error",
		Data:  string(errorString),
	}); err != nil {
		common.LogError("WriteJSON error: %v", err)
		return
	}
}

func (t *threadSafeWriter) WriteJSON(v interface{}) error {
	t.Lock()
	defer t.Unlock()

	return t.Conn.WriteJSON(v)
}
