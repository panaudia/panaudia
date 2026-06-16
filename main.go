package main

import (
	"crypto/tls"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fabritsius/envar"
	"github.com/joho/godotenv"
	"github.com/panaudia/panaudia/core/binaural"
	"github.com/panaudia/panaudia/core/commands"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/moq"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/panaudia/panaudia/core/timing"
	"github.com/panaudia/panaudia/direct"
	"github.com/panaudia/panaudia/directroc"
	"github.com/panaudia/panaudia/fixtures"
	"github.com/panaudia/panaudia/unified"
)

var (
	cfg config
)

// version is compiled in from the repo's top-level `version` file (the same
// file the docker build scripts tag images with), so the startup banner and
// the image tag never drift.
//
//go:embed version
var version string

type config struct {
	Host          string `env:"PANAUDIA_HOST" default:"0.0.0.0"`
	Port          int    `env:"PANAUDIA_PORT" default:"4443"`
	GoMaxProcs    int    `env:"PANAUDIA_GOMAXPROCS" default:"4"`
	ICEHost       string `env:"PANAUDIA_ICE_HOST" default:""`
	ICEPort       int    `env:"PANAUDIA_ICE_PORT" default:"0"`
	TLSCrtPath    string `env:"PANAUDIA_TLS_CTR_PATH" default:"keys/server.crt"`
	TLSKeyPath    string `env:"PANAUDIA_TLS_KEY_PATH" default:"keys/server.key"`
	TicketKeyPath string `env:"PANAUDIA_TICKET_KEY_PATH" default:"keys/panaudia_key.pub"`
	Unticketed    int    `env:"PANAUDIA_UNTICKETED" default:"1"`
	SpaceSize     int    `env:"PANAUDIA_SPACE_SIZE" default:"40"`
	SpaceOrder    int    `env:"PANAUDIA_SPACE_ORDER" default:"3"`
	MaxSources    int    `env:"PANAUDIA_SPACE_MAX_SOURCES" default:"128"`
	ReverbPreset  int    `env:"PANAUDIA_REVERB_PRESET" default:"0"`
	EnableLinkIn  int    `env:"PANAUDIA_ENABLE_LINK_IN" default:"0"`
	EnableLinkOut int    `env:"PANAUDIA_ENABLE_LINK_OUT" default:"0"`
	LinkPort      int    `env:"PANAUDIA_LINK_PORT" default:"80"`
	LogMs         int    `env:"PANAUDIA_LOG_MS" default:"0"`
	TestTone      int    `env:"PANAUDIA_TEST_TONE" default:"0"`
	TestPeople    int    `env:"PANAUDIA_TEST_PEOPLE" default:"0"`
	TestVoices    int    `env:"PANAUDIA_TEST_VOICES" default:"0"`
	StereoTest    int    `env:"PANAUDIA_STEREO_TEST" default:"0"`
	LogLevel      int    `env:"PANAUDIA_LOG_LEVEL" default:"2"`
}

// loadDotEnv loads variables from a .env file into the process environment, if
// the file is present. The path defaults to ".env" in the working directory and
// can be overridden with PANAUDIA_ENV_FILE (an actual environment variable, so
// it has to be set the normal way). A missing file is not an error; a malformed
// one panics, consistent with how the rest of config loading fails fast.
func loadDotEnv() {
	path := os.Getenv("PANAUDIA_ENV_FILE")
	if path == "" {
		path = ".env"
	}
	if _, err := os.Stat(path); err != nil {
		return // no .env file — rely on the environment and struct defaults
	}
	if err := godotenv.Load(path); err != nil {
		panic(fmt.Errorf("loading %s: %w", path, err))
	}
}

func main() {
	// Optionally seed the environment from a .env file before reading config.
	// The file is optional: a missing .env is fine (env vars / defaults still
	// apply), but a malformed one fails fast. godotenv.Load (not Overload)
	// never overwrites an already-set variable, so precedence stays:
	// real environment > .env file > struct defaults.
	loadDotEnv()

	cfg = config{}
	if err := envar.Fill(&cfg); err != nil {
		panic(err)
	}

	runtime.GOMAXPROCS(cfg.GoMaxProcs)

	logMs := cfg.LogMs == 1
	testTone := cfg.TestTone == 1
	unticketed := cfg.Unticketed == 1
	enableLinkIn := cfg.EnableLinkIn == 1
	enableLinkOut := cfg.EnableLinkOut == 1

	order := cfg.SpaceOrder
	// ROC ambisonic output only supports order 2 or 3; without it the
	// renderer accepts any order 2..5.
	if enableLinkOut {
		if !(order == 2 || order == 3) {
			panic("PANAUDIA_SPACE_ORDER must be 2 or 3 when using PANAUDIA_ENABLE_LINK_OUT")
		}
	} else {
		if order < 2 || order > 5 {
			panic("PANAUDIA_SPACE_ORDER must be from 2 to 5")
		}
	}

	fmt.Printf("\n\n")
	fmt.Printf("     ______   ___   _   _   ___   _   _ ______  _____  ___ \n")
	fmt.Printf("     | ___ \\ / _ \\ | \\ | | / _ \\ | | | ||  _  \\|_   _|/ _ \\ \n")
	fmt.Printf("     | |_/ // /_\\ \\|  \\| |/ /_\\ \\| | | || | | |  | | / /_\\ \\\n")
	fmt.Printf("     |  __/ |  _  || . ` ||  _  || | | || | | |  | | |  _  |\n")
	fmt.Printf("     | |    | | | || |\\  || | | || |_| || |/ /  _| |_| | | |\n")
	fmt.Printf("     \\_|    \\_| |_/\\_| \\_/\\_| |_/ \\___/ |___/   \\___/\\_| |_/\n\n")

	fmt.Printf("\n             --- A Network Spatial Audio Engine ---\n")
	fmt.Printf("\n                      https://panaudia.com\n")

	fmt.Printf("\n                    v%s (Unified MOQ+WebRTC+ROC)\n\n", strings.TrimSpace(version))

	fmt.Printf("-----------------------------------------------------------------\n")
	fmt.Printf("Config \n")
	fmt.Printf("-----------------------------------------------------------------\n")
	fmt.Printf("  PANAUDIA_HOST:               %v\n", cfg.Host)
	fmt.Printf("  PANAUDIA_PORT:               %d\n", cfg.Port)
	fmt.Printf("  PANAUDIA_GOMAXPROCS:         %d\n", cfg.GoMaxProcs)
	fmt.Printf("  PANAUDIA_ICE_HOST:           %v\n", cfg.ICEHost)
	fmt.Printf("  PANAUDIA_ICE_PORT:           %d\n", cfg.ICEPort)
	fmt.Printf("  PANAUDIA_TLS_CTR_PATH:       %v\n", cfg.TLSCrtPath)
	fmt.Printf("  PANAUDIA_TLS_KEY_PATH:       %v\n", cfg.TLSKeyPath)
	fmt.Printf("  PANAUDIA_TICKET_KEY_PATH:    %v\n", cfg.TicketKeyPath)
	fmt.Printf("  PANAUDIA_UNTICKETED:         %d\n", cfg.Unticketed)
	fmt.Printf("  PANAUDIA_SPACE_SIZE:         %v\n", cfg.SpaceSize)
	fmt.Printf("  PANAUDIA_SPACE_ORDER:        %d\n", order)
	fmt.Printf("  PANAUDIA_SPACE_MAX_SOURCES:  %d\n", cfg.MaxSources)
	fmt.Printf("  PANAUDIA_REVERB_PRESET:      %d\n", cfg.ReverbPreset)
	fmt.Printf("  PANAUDIA_ENABLE_LINK_IN:     %d\n", cfg.EnableLinkIn)
	fmt.Printf("  PANAUDIA_ENABLE_LINK_OUT:    %d\n", cfg.EnableLinkOut)
	fmt.Printf("  PANAUDIA_LINK_PORT:          %d\n", cfg.LinkPort)
	fmt.Printf("  PANAUDIA_LOG_MS:             %d\n", cfg.LogMs)
	fmt.Printf("  PANAUDIA_TEST_TONE:          %d\n", cfg.TestTone)
	fmt.Printf("  PANAUDIA_TEST_PEOPLE:        %d\n", cfg.TestPeople)
	fmt.Printf("  PANAUDIA_TEST_VOICES:        %d\n", cfg.TestVoices)
	fmt.Printf("  PANAUDIA_STEREO_TEST:        %d\n", cfg.StereoTest)
	fmt.Printf("  PANAUDIA_LOG_LEVEL:          %v\n", cfg.LogLevel)

	fmt.Printf("-----------------------------------------------------------------\n")
	fmt.Printf("Transport \n")
	fmt.Printf("-----------------------------------------------------------------\n")
	fmt.Printf("  UDP %d:  QUIC → MOQ (WebTransport + raw QUIC)\n", cfg.Port)
	fmt.Printf("  TCP %d:  TLS  → HTTPS/WebSocket (signaling + WebRTC)\n", cfg.Port)
	fmt.Printf("  TCP %d:  STUN → ICE TCP (WebRTC media)\n", cfg.Port)
	if enableLinkIn {
		fmt.Printf("  WS  %d:    /roc-input  → Panaudia Link in (RTP on own ports)\n", cfg.LinkPort)
	}
	if enableLinkOut {
		fmt.Printf("  WS  %d:    /roc-output → Panaudia Link out (RTP on own ports)\n", cfg.LinkPort)
	}
	fmt.Printf("  Authentication:     JWT\n")

	fmt.Printf("-----------------------------------------------------------------\n")
	fmt.Printf("Rendering \n")
	fmt.Printf("-----------------------------------------------------------------\n")
	fmt.Printf("  Max Sources:        %d\n", cfg.MaxSources)
	fmt.Printf("  Sample Rate:        48kHz\n")
	fmt.Printf("  Bit Depth:          32-bit\n")
	fmt.Printf("  Ambisonic Order:    %d\n", order)
	fmt.Printf("  Binaural Decoder:   %v\n", binaural.DECODING_METHOD_NAMES[binaural.AMBI_BIN_DECODING_METHOD])
	fmt.Printf("  HRIR:               KEMAR Dummy Head\n")
	fmt.Printf("-----------------------------------------------------------------\n")

	common.SetLogLevel(cfg.LogLevel)

	// --- Shared spatial mixer ---
	common.LogInfo("Initialising spatial mixer...")

	if cfg.StereoTest == 1 {
		inout.SetStereoTestTone(true)
		common.LogInfo("PANAUDIA_STEREO_TEST: replacing binaural output with 440Hz L / 880Hz R test tones")
	}

	// DirectRocBackend embeds DirectBackend: it serves MOQ + WebRTC
	// identically to the plain backend, and additionally mints ROC
	// connection handlers when Panaudia Link is enabled.
	backend := directroc.NewDirectRocBackend(common.ChannelCountForOrder(order), cfg.MaxSources)
	authoriser := direct.NewDirectAuthoriser(cfg.TicketKeyPath, order, commands.DefaultAuthorizer(), backend.KickGate)
	s := direct.NewDefaultDirectSpace(float64(cfg.SpaceSize), order, cfg.ReverbPreset, cfg.MaxSources)
	backend.SetSpace(s)
	s.SourceManager = backend
	backend.StartReconciler(direct.DefaultReconcilePeriod)

	// NullOut nodes (performance-test "people") render through a real
	// binaural decode-and-discard output rather than the no-op
	// StereoNullOutput, so they exercise the binaural render path. Decoders
	// come from the same pre-built pool as live connections; total outputs
	// are capped at cfg.MaxSources = pool size, so it cannot be starved.
	s.NullOutputFactory = func(channelCount int) inout.AmbisonicOutput {
		return inout.NewBinauralNullOutput(
			backend.BinauralDecoderPool.GetDecoder(common.Rotation{}),
			channelCount)
	}

	// Graceful shutdown (plan/history/state-cleanup phase 5, E9): drain all
	// live sessions — each gets the full announced departure — before
	// the process exits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		common.LogInfo("Signal received — draining sessions and shutting down")
		backend.Shutdown(5 * time.Second)
		os.Exit(0)
	}()

	if testTone {
		fixtures.AddTestTone(s)
	}

	// Performance test fixtures. "People" are listeners — each gets a full
	// per-listener ambisonic mix AND a binaural decode-and-discard output
	// (via s.NullOutputFactory above), so they exercise both the mixing and
	// the binaural render paths. "Voices" are pure sources that add to
	// everyone's mix. Both are added at startup, before audio flows.
	// Both "people" and "voices" are nodes occupying source slots, and the
	// mixer / per-node PeerEncoders / decoder pool are all sized to
	// MaxSources. Adding more nodes than that overruns those fixed-size
	// allocations (render-time out-of-bounds). Clamp the combined fixture
	// count to MaxSources, keeping as many people as possible (they exercise
	// both mixing and binaural) and giving the remainder to voices.
	testPeople := cfg.TestPeople
	testVoices := cfg.TestVoices
	if testPeople > cfg.MaxSources {
		common.LogWarn("PANAUDIA_TEST_PEOPLE %d exceeds PANAUDIA_SPACE_MAX_SOURCES %d — clamping to %d",
			testPeople, cfg.MaxSources, cfg.MaxSources)
		testPeople = cfg.MaxSources
	}
	if testPeople+testVoices > cfg.MaxSources {
		clamped := cfg.MaxSources - testPeople
		common.LogWarn("PANAUDIA_TEST_PEOPLE %d + PANAUDIA_TEST_VOICES %d exceeds PANAUDIA_SPACE_MAX_SOURCES %d — clamping voices to %d",
			testPeople, testVoices, cfg.MaxSources, clamped)
		testVoices = clamped
	}

	if testPeople > 0 {
		common.LogInfo("Adding %d test people", testPeople)
		fixtures.AddRandomPeople(s, testPeople)
	}
	if testVoices > 0 {
		common.LogInfo("Adding %d test voices", testVoices)
		fixtures.AddRandomInstruments(s, testVoices)
	}

	// --- Spatial processing loop (5ms ticks) ---
	common.LogInfo("Starting spatial processing loop...")
	go func() {
		s.Process(false)

		ticker := timing.NewTicker(5, false)

		secondCounter := 0
		secondCounterLimit := 200

		now := time.Now()
		noTime := now.Sub(now)
		totalSecondTook := noTime

		for {
			s.Process(false)

			took := ticker.Tick()
			totalSecondTook += took

			secondCounter++
			if secondCounter == secondCounterLimit {
				ms := int(totalSecondTook.Milliseconds())

				if logMs {
					fmt.Printf("ms: %d\n", ms)
				}
				if ms > 1000 {
					fmt.Printf("WARNING: Render took too long %dms out of 1000\n", ms)
				}

				secondCounter = 0
				totalSecondTook = noTime
			}
		}
	}()

	// --- MOQ server on UDP ---
	common.LogInfo("Creating MOQ server...")
	moqConfig := moq.MoqServerConfig{
		Host:       cfg.Host,
		Port:       cfg.Port,
		TLSCrt:     cfg.TLSCrtPath,
		TLSKey:     cfg.TLSKeyPath,
		MaxClients: cfg.MaxSources,
		Unticketed: unticketed,
	}

	moqServer, err := moq.NewMoqServer(moqConfig, backend, authoriser)
	if err != nil {
		panic(fmt.Sprintf("Failed to create MOQ server: %v", err))
	}

	common.LogInfo("Starting MOQ server on UDP %s:%d...", cfg.Host, cfg.Port)
	if err := moqServer.Start(); err != nil {
		panic(fmt.Sprintf("Failed to start MOQ server: %v", err))
	}

	// --- TCP demuxer ---
	tcpAddr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	common.LogInfo("Starting TCP listener on %s...", tcpAddr)

	tcpListener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		panic(fmt.Sprintf("Failed to listen on TCP %s: %v", tcpAddr, err))
	}

	demuxer := unified.NewDemuxer(tcpListener)
	tlsListener := demuxer.TLSListener()
	stunListener := demuxer.STUNListener()

	go demuxer.Run()

	// --- WebRTC setup (ICE TCP via STUN listener) ---
	common.LogInfo("Setting up WebRTC with ICE TCP on port %d...", cfg.Port)
	webrtcServer := unified.NewWebRTCServer(unified.WebRTCConfig{
		ICEHost:    cfg.ICEHost,
		ICEPort:    cfg.ICEPort,
		Unticketed: unticketed,
	}, backend, authoriser, stunListener)

	// --- HTTPS server on demuxer's TLS listener ---
	common.LogInfo("Starting HTTPS server on TCP %s...", tcpAddr)

	cert, err := tls.LoadX509KeyPair(cfg.TLSCrtPath, cfg.TLSKeyPath)
	if err != nil {
		panic(fmt.Sprintf("Failed to load TLS certificate: %v", err))
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	mux := http.NewServeMux()

	// Health / greeting endpoints
	mux.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello from Panaudia\n"))
	})

	// WebRTC signaling
	mux.HandleFunc("/join", webrtcServer.JoinHandler)

	// --- Panaudia Link (ROC) signalling, opt-in via env flags ---
	// ROC media flows over its own negotiated RTP/UDP ports; the signalling
	// websocket runs on its own dedicated plain-HTTP port (PANAUDIA_LINK_PORT,
	// default 80) — NOT the shared TLS port. No auth on this path by design
	// (WireGuard-wrapped in cloud; query-param in spatial), so plain ws:// is
	// fine. Note: binding port 80 needs root / CAP_NET_BIND_SERVICE.
	if enableLinkIn || enableLinkOut {
		rocSignaling := panaudia_server.NewRocSignaling(backend, authoriser)
		rocMux := http.NewServeMux()
		if enableLinkIn {
			rocMux.HandleFunc("/roc-input", rocSignaling.RocInHandler)
		}
		if enableLinkOut {
			rocMux.HandleFunc("/roc-output", rocSignaling.RocOutHandler)
		}

		rocAddr := fmt.Sprintf("%s:%d", cfg.Host, cfg.LinkPort)
		rocListener, rocErr := net.Listen("tcp", rocAddr)
		if rocErr != nil {
			panic(fmt.Sprintf("Failed to listen on TCP %s for Panaudia Link: %v", rocAddr, rocErr))
		}

		rocServer := &http.Server{Handler: rocMux}
		common.LogInfo("Panaudia Link (ROC) enabled — in:%v out:%v on plain HTTP %s", enableLinkIn, enableLinkOut, rocAddr)
		go func() {
			if err := rocServer.Serve(rocListener); err != nil {
				common.LogError("Panaudia Link (ROC) server error: %v", err)
			}
		}()
	}

	// Alt-Svc middleware: tell browsers about HTTP/3 on same port
	altSvcHeader := fmt.Sprintf(`h3=":%d"; ma=86400`, cfg.Port)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Alt-Svc", altSvcHeader)
		mux.ServeHTTP(w, r)
	})

	httpsServer := &http.Server{
		Handler:   handler,
		TLSConfig: tlsConfig,
	}

	common.LogInfo("Unified server running on port %d (UDP: MOQ, TCP: HTTPS+WebRTC)", cfg.Port)
	common.LogInfo("  curl -k https://localhost:%d/probe", cfg.Port)
	common.LogInfo("Press Ctrl+C to stop.")

	// ServeTLS with an already-accepted listener — TLS handshake happens here
	if err := httpsServer.ServeTLS(tlsListener, "", ""); err != nil {
		common.LogError("HTTPS server error: %v", err)
	}

	_ = stunListener // kept alive by demuxer goroutine
}
