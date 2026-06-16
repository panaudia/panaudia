package moq

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

// debugConn wraps a moqtransport.Connection to log all method calls
type debugConn struct {
	inner moqtransport.Connection
	name  string
}

// debugStream wraps a stream to log reads and writes
type debugStream struct {
	inner moqtransport.Stream
	name  string
}

func (ds *debugStream) Read(p []byte) (int, error) {
	n, err := ds.inner.Read(p)
	common.LogDebug("[%s] Stream.Read: %d bytes, err=%v", ds.name, n, err)
	if n > 0 && n <= 512 {
		common.LogDebug("[%s] Stream.Read data: %x", ds.name, p[:n])
	}
	return n, err
}

func (ds *debugStream) Write(p []byte) (int, error) {
	common.LogDebug("[%s] Stream.Write: %d bytes", ds.name, len(p))
	if len(p) <= 64 {
		common.LogDebug("[%s] Stream.Write data: %x", ds.name, p)
	}
	n, err := ds.inner.Write(p)
	common.LogDebug("[%s] Stream.Write returned: n=%d, err=%v", ds.name, n, err)
	return n, err
}

func (ds *debugStream) Close() error {
	common.LogDebug("[%s] Stream.Close called", ds.name)
	return ds.inner.Close()
}

func (ds *debugStream) Reset(code uint32) {
	common.LogDebug("[%s] Stream.Reset called with code %d", ds.name, code)
	ds.inner.Reset(code)
}

func (ds *debugStream) Stop(code uint32) {
	common.LogDebug("[%s] Stream.Stop called with code %d", ds.name, code)
	ds.inner.Stop(code)
}

func (ds *debugStream) StreamID() uint64 {
	return ds.inner.StreamID()
}

func (d *debugConn) AcceptStream(ctx context.Context) (moqtransport.Stream, error) {
	common.LogDebug("[%s] AcceptStream called", d.name)
	s, err := d.inner.AcceptStream(ctx)
	if err != nil {
		common.LogDebug("[%s] AcceptStream returned error: %v", d.name, err)
		return nil, err
	}
	common.LogDebug("[%s] AcceptStream returned stream, wrapping with debug", d.name)
	return &debugStream{inner: s, name: d.name + "-ctrl"}, nil
}

func (d *debugConn) AcceptUniStream(ctx context.Context) (moqtransport.ReceiveStream, error) {
	common.LogDebug("[%s] AcceptUniStream called", d.name)
	s, err := d.inner.AcceptUniStream(ctx)
	if err != nil {
		common.LogDebug("[%s] AcceptUniStream returned error: %v", d.name, err)
	} else {
		common.LogDebug("[%s] AcceptUniStream returned stream", d.name)
	}
	return s, err
}

func (d *debugConn) OpenStream() (moqtransport.Stream, error) {
	common.LogDebug("[%s] OpenStream called", d.name)
	return d.inner.OpenStream()
}

func (d *debugConn) OpenStreamSync(ctx context.Context) (moqtransport.Stream, error) {
	common.LogDebug("[%s] OpenStreamSync called", d.name)
	return d.inner.OpenStreamSync(ctx)
}

func (d *debugConn) OpenUniStream() (moqtransport.SendStream, error) {
	common.LogDebug("[%s] OpenUniStream called", d.name)
	return d.inner.OpenUniStream()
}

func (d *debugConn) OpenUniStreamSync(ctx context.Context) (moqtransport.SendStream, error) {
	common.LogDebug("[%s] OpenUniStreamSync called", d.name)
	return d.inner.OpenUniStreamSync(ctx)
}

func (d *debugConn) SendDatagram(b []byte) error {
	//common.LogDebug("[%s] SendDatagram called, len=%d", d.name, len(b))
	return d.inner.SendDatagram(b)
}

func (d *debugConn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	b, err := d.inner.ReceiveDatagram(ctx)
	if err != nil {
		common.LogError("[%s] ReceiveDatagram error: %v", d.name, err)
	}
	return b, err
}

func (d *debugConn) CloseWithError(e uint64, msg string) error {
	common.LogDebug("[%s] CloseWithError called: %d %s", d.name, e, msg)
	return d.inner.CloseWithError(e, msg)
}

func (d *debugConn) Context() context.Context {
	return d.inner.Context()
}

func (d *debugConn) Perspective() moqtransport.Perspective {
	p := d.inner.Perspective()
	common.LogDebug("[%s] Perspective called, returning %v", d.name, p)
	return p
}

func (d *debugConn) Protocol() moqtransport.Protocol {
	return d.inner.Protocol()
}

// NegotiatedALPN returns the ALPN/WebTransport subprotocol Eyevinn's Session uses to
// select the MoQ draft version (e.g. "moqt-16" → draft-16).
//
// We mandate draft-16 on both ends. Over WebTransport, the negotiated subprotocol is
// only available to browsers that implement WT subprotocol negotiation (Chrome → it
// reports "moqt-16"); Firefox and current Safari DON'T, so the inner conn returns ""
// → Eyevinn would fall back to in-band draft-14 negotiation, which our draft-16-only
// client doesn't speak → the server rejects CLIENT_SETUP and closes (client sees
// "remote WebTransport close"). Since the whole stack is draft-16, force "moqt-16" for
// any WebTransport connection that came back empty. Raw-QUIC (ALPN-negotiated) and
// browsers that did negotiate the subprotocol are left untouched.
func (d *debugConn) NegotiatedALPN() string {
	alpn := d.inner.NegotiatedALPN()
	if alpn == "" && d.inner.Protocol() == moqtransport.ProtocolWebTransport {
		return "moqt-16"
	}
	return alpn
}

// MoqServerConfig holds configuration for the MOQ server
type MoqServerConfig struct {
	Host       string
	Port       int
	TLSCrt     string
	TLSKey     string
	MaxClients int
	// Unticketed allows clients to connect without a JWT ticket. When enabled,
	// SUBSCRIBE messages without an Authorization parameter are authenticated
	// via the authoriser's AuthoriseWithoutTicket() path using only the
	// WebTransport upgrade URL's query parameters (e.g. uuid=…).
	Unticketed bool
}

// Backend interface that the MOQ server uses to interact with the mixer
// For simplicity, we define only the subset we need
type Backend interface {
	NewConnectionHandler(nodeConfig common.NodeConfig, outputTrack panaudia_server.TrackWriter) panaudia_server.ConnectionHandler
	FreeSource(uuid uuid.UUID)
}

// Authoriser interface for JWT authentication
type Authoriser interface {
	// Authorise validates JWT from query parameters and returns NodeConfig
	Authorise(queryValues map[string][]string) (common.NodeConfig, error)
	// AuthoriseWithoutTicket builds a NodeConfig from query parameters alone
	// (no JWT). Only called when MoqServerConfig.Unticketed is true.
	AuthoriseWithoutTicket(queryValues map[string][]string) (common.NodeConfig, error)
}

// MoqServer manages MOQ connections and sessions
type MoqServer struct {
	config     MoqServerConfig
	backend    Backend
	authoriser Authoriser

	listener   *quic.Listener
	tlsConfig  *tls.Config
	quicConfig *quic.Config

	// WebTransport server for browser clients
	wtServer *webtransport.Server

	sessions   map[string]*MoqSession
	sessionsMu sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc
}

// NewMoqServer creates a new MOQ server instance
func NewMoqServer(config MoqServerConfig, backend Backend, authoriser Authoriser) (*MoqServer, error) {
	ctx, cancel := context.WithCancel(context.Background())

	server := &MoqServer{
		config:     config,
		backend:    backend,
		authoriser: authoriser,
		sessions:   make(map[string]*MoqSession),
		ctx:        ctx,
		cancel:     cancel,
	}

	// Initialize TLS configuration
	if err := server.initTLS(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize TLS: %w", err)
	}

	// Initialize QUIC configuration
	server.initQUIC()

	return server, nil
}

// Start begins listening for MOQ connections
func (s *MoqServer) Start() error {
	address := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)

	common.LogInfo("Starting MOQ server on %s", address)

	// Create UDP listener for QUIC
	udpAddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on UDP: %w", err)
	}

	// Create QUIC listener
	listener, err := quic.Listen(udpConn, s.tlsConfig, s.quicConfig)
	if err != nil {
		return fmt.Errorf("failed to create QUIC listener: %w", err)
	}
	s.listener = listener

	// Create per-server HTTP mux so multiple MoqServer instances in the
	// same process (notably under test) don't collide on the package-
	// global http.DefaultServeMux.
	mux := http.NewServeMux()
	mux.HandleFunc("/moq", s.handleWebTransportUpgrade)

	// Create WebTransport server for browser clients (HTTP/3)
	s.wtServer = &webtransport.Server{
		H3: &http3.Server{
			Addr:            address,
			TLSConfig:       s.tlsConfig,
			Handler:         withCrossOriginIsolation(mux),
			EnableDatagrams: true, // WebTransport datagrams (audio) over HTTP/3
		},
		// Offer the draft-16 subprotocol so the browser can negotiate it via
		// WT-Available-Protocols. Eyevinn reads the negotiated value from
		// SessionState().ApplicationProtocol to select draft-16 over WebTransport;
		// without this it would fall back to in-band draft-14 negotiation.
		ApplicationProtocols: []string{"moqt-16"},
		// Allow cross-origin requests (web page may be served from different port/host)
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	// Advertise WebTransport support in the HTTP/3 SETTINGS frame. Because we
	// drive the H3 server via our own QUIC listener (ServeQUICConn) rather than
	// webtransport.Server.Serve, this is not done automatically — without it the
	// client reports "server didn't enable WebTransport".
	webtransport.ConfigureHTTP3Server(s.wtServer.H3)

	// Safari compatibility (2026-06-11): updated Safari cancels its CONNECT
	// right after reading our SETTINGS (qlog: stop_sending/reset_stream 0x10c)
	// because webtransport-go v0.10.0 only advertises the draft-02..06 ENABLE
	// flag (0x2b603742). Newer drafts gate session establishment on different
	// SETTINGS: draft-07..12 require SETTINGS_WEBTRANSPORT_MAX_SESSIONS
	// (0xc671706a) > 0; draft-13+ require SETTINGS_WT_ENABLED (0x2c7cf000) = 1.
	// Advertise both — unknown SETTINGS are must-ignore per RFC 9114, so this
	// is harmless for Chrome/Firefox/raw-QUIC.
	s.wtServer.H3.AdditionalSettings[0xc671706a] = 64 // WEBTRANSPORT_MAX_SESSIONS (draft-07..12)
	s.wtServer.H3.AdditionalSettings[0x2c7cf000] = 1  // WT_ENABLED (draft-13+)

	// Draft-13+ session-level flow control: the client's credit to open streams
	// / send data INSIDE the WebTransport session comes from these SETTINGS,
	// and they default to 0 — a conforming client (updated Safari) therefore
	// queues its MOQ control-stream open forever and auth never starts (server
	// parked in AcceptStream). webtransport-go v0.10.0 predates these and never
	// tracks or extends them via capsules, so advertise effectively-unlimited
	// values: the client can never hit the ceiling, and the missing capsule
	// machinery never matters. Audio datagrams are outside this flow control.
	s.wtServer.H3.AdditionalSettings[0x2b65] = 1024    // WT_INITIAL_MAX_STREAMS_BIDI
	s.wtServer.H3.AdditionalSettings[0x2b64] = 1024    // WT_INITIAL_MAX_STREAMS_UNI
	s.wtServer.H3.AdditionalSettings[0x2b61] = 1 << 40 // WT_INITIAL_MAX_DATA (~1 TB)

	common.LogInfo("MOQ server listening on %s (QUIC + WebTransport)", address)

	// Accept connections in a loop
	go s.acceptLoop()

	return nil
}

// acceptLoop continuously accepts new QUIC connections
func (s *MoqServer) acceptLoop() {
	for {
		select {
		case <-s.ctx.Done():
			common.LogInfo("MOQ server shutting down")
			return
		default:
		}

		// Accept a new QUIC connection
		conn, err := s.listener.Accept(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil {
				// Context cancelled, shutting down
				return
			}
			common.LogError("Failed to accept QUIC connection: %v", err)
			continue
		}

		// Check the negotiated ALPN protocol
		protocol := conn.ConnectionState().TLS.NegotiatedProtocol
		common.LogDebug("New QUIC connection from %s (ALPN: %s)", conn.RemoteAddr(), protocol)

		switch protocol {
		case "h3":
			// HTTP/3 connection - route to WebTransport server
			// This handles browser WebTransport clients
			go s.wtServer.ServeQUICConn(conn)
		case "moqt-16", "moq-00":
			// Raw QUIC MOQ connection - handle directly. "moqt-16" selects
			// draft-16 (ALPN-negotiated); "moq-00" is retained for draft-14
			// in-band negotiation. Eyevinn picks the version from the ALPN.
			go s.handleQuicConnection(conn)
		default:
			common.LogWarn("Unknown ALPN protocol: %s from %s", protocol, conn.RemoteAddr())
			conn.CloseWithError(0, "unsupported protocol")
		}
	}
}

// withCrossOriginIsolation sets the COOP/COEP headers that make a served HTML
// document "cross-origin isolated" (self.crossOriginIsolated === true), which is
// what unlocks SharedArrayBuffer in the browser. The TS MOQ playout needs SAB for
// its real-time-safe worker→worklet audio ring; without isolation it falls back
// to a main-thread-coupled postMessage path that crackles under load. See
// spatial-mixer/plan/history/browser-audio/playout-v3-design.md §11.9.
//
// NOTE: these headers only take effect on the HTML *document* response, so they
// matter only when this server (or whatever shares its origin) serves the page.
// They are harmless on the /moq WebTransport upgrade (a CONNECT, not a document)
// and on any non-document route. COEP: require-corp means same-origin's
// cross-origin *subresources* must send CORP/CORS — WebTransport is a connection,
// not a subresource, so the /moq endpoint is unaffected.
func withCrossOriginIsolation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Embedder-Policy", "require-corp")
		next.ServeHTTP(w, r)
	})
}

// handleWebTransportUpgrade handles HTTP requests to upgrade to WebTransport
func (s *MoqServer) handleWebTransportUpgrade(w http.ResponseWriter, r *http.Request) {
	common.LogDebug("WebTransport upgrade request from %s", r.RemoteAddr)

	session, err := s.wtServer.Upgrade(w, r)
	if err != nil {
		common.LogError("WebTransport upgrade failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	common.LogInfo("WebTransport session established from %s", r.RemoteAddr)

	// Handle the WebTransport session as a MOQ connection
	go s.handleWebTransportSession(session, r.RemoteAddr, r.URL.Query())
}

// handleWebTransportSession processes a WebTransport session as MOQ
func (s *MoqServer) handleWebTransportSession(wtSession *webtransport.Session, remoteAddr string, queryParams map[string][]string) {
	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			common.LogError("Panic in WebTransport handler for %s: %v", remoteAddr, r)
		}
	}()

	// Wrap WebTransport session for MOQ
	common.LogDebug("Wrapping WebTransport session for MOQ (remoteAddr: %s), session ptr: %p", remoteAddr, wtSession)
	rawConn := webtransportmoq.NewServer(wtSession)
	moqConn := &debugConn{inner: rawConn, name: remoteAddr}
	common.LogDebug("WebTransport wrapper created with debug, perspective: server")

	// Create our session wrapper
	session := &MoqSession{
		conn:         nil, // WebTransport doesn't use raw QUIC conn
		server:       s,
		queryParams:  queryParams,
		transportCtx: wtSession.Context(),
		closeTransport: func(reason string) {
			_ = wtSession.CloseWithError(0, reason)
		},
		transportName: "moq-wt",
	}

	// Create MOQ session for this connection
	// Set up the SubscribeHandler for authentication
	subscribeHandler := NewSessionSubscribeHandler(session, s.authoriser, s.config.Unticketed)
	// Set up the generic Handler for ANNOUNCE, SUBSCRIBE_ANNOUNCES, etc.
	sessionHandler := NewSessionHandler(session)
	moqSession := &moqtransport.Session{
		Handler:             sessionHandler,
		SubscribeHandler:    subscribeHandler,
		InitialMaxRequestID: 100,
	}

	// Link the MOQ session to our session wrapper
	session.moqSession = moqSession

	common.LogInfo("Starting MOQ session for WebTransport client %s", remoteAddr)
	common.LogDebug("MOQ session config: InitialMaxRequestID=%d", moqSession.InitialMaxRequestID)

	// Register the session
	s.registerSession(remoteAddr, session)
	defer func() {
		s.unregisterSession(remoteAddr)
		if err := session.Close(); err != nil {
			logCloseError("Error closing session for %s: %v", err, remoteAddr)
		}
	}()

	// Run the MOQ session handshake
	common.LogDebug("Calling moqSession.Run() for %s...", remoteAddr)
	if err := moqSession.Run(moqConn); err != nil {
		common.LogError("MOQ session error for WebTransport client %s: %v", remoteAddr, err)
		return
	}
	common.LogDebug("moqSession.Run() handshake completed for %s, session is now active", remoteAddr)

	// IMPORTANT: moqSession.Run() returns after the handshake completes, NOT when the session ends.
	// The session's goroutines continue running in the background.
	// We must wait for the WebTransport session to close before cleaning up.
	common.LogInfo("MOQ session active for WebTransport client %s, waiting for disconnect...", remoteAddr)
	<-wtSession.Context().Done()

	// Close the MOQ session and log the errgroup error (captures parse/dispatch errors)
	if closeErr := moqSession.Close(); closeErr != nil {
		logCloseError("MOQ session errgroup error for %s: %v", closeErr, remoteAddr)
	}

	common.LogInfo("WebTransport MOQ connection closed: %s", remoteAddr)
}

// handleQuicConnection processes a raw QUIC MOQ connection (non-WebTransport)
func (s *MoqServer) handleQuicConnection(conn *quic.Conn) {
	remoteAddr := (*conn).RemoteAddr().String()

	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			common.LogError("Panic in MOQ connection handler for %s: %v", remoteAddr, r)
		}
	}()

	// Ensure connection is closed
	defer func() {
		if err := (*conn).CloseWithError(0, "connection closed"); err != nil {
			common.LogDebug("Error closing connection %s: %v", remoteAddr, err)
		}
	}()

	// Wrap QUIC connection for MOQ
	moqConn := quicmoq.NewServer(conn)

	// Create our session wrapper first (needed for subscribe handler)
	session := &MoqSession{
		conn:         conn,
		server:       s,
		transportCtx: (*conn).Context(),
		closeTransport: func(reason string) {
			_ = (*conn).CloseWithError(0, reason)
		},
		transportName: "moq-quic",
	}

	// Create MOQ session for this connection
	// Set up the SubscribeHandler for authentication
	subscribeHandler := NewSessionSubscribeHandler(session, s.authoriser, s.config.Unticketed)
	// Set up the generic Handler for ANNOUNCE, SUBSCRIBE_ANNOUNCES, etc.
	sessionHandler := NewSessionHandler(session)
	moqSession := &moqtransport.Session{
		Handler:             sessionHandler,
		SubscribeHandler:    subscribeHandler,
		InitialMaxRequestID: 100,
	}

	// Link the MOQ session to our session wrapper
	session.moqSession = moqSession

	common.LogInfo("Starting MOQ session for %s", remoteAddr)

	// Register the session
	s.registerSession(remoteAddr, session)
	defer func() {
		// Ensure session cleanup happens even on panic
		s.unregisterSession(remoteAddr)
		if err := session.Close(); err != nil {
			logCloseError("Error closing session for %s: %v", err, remoteAddr)
		}
	}()

	// Run the MOQ session handshake
	// Note: Run() returns after handshake completes, not when session ends
	if err := moqSession.Run(moqConn); err != nil {
		common.LogError("MOQ session error for %s: %v", remoteAddr, err)
		return
	}

	// Wait for the QUIC connection to close
	common.LogInfo("MOQ session active for %s, waiting for disconnect...", remoteAddr)
	<-(*conn).Context().Done()

	// Close the MOQ session and log the errgroup error (captures parse/dispatch errors)
	if closeErr := moqSession.Close(); closeErr != nil {
		logCloseError("MOQ session errgroup error for %s: %v", closeErr, remoteAddr)
	}

	common.LogInfo("MOQ connection closed: %s", remoteAddr)
}

// registerSession adds a session to the server's session map
func (s *MoqServer) registerSession(id string, session *MoqSession) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	s.sessions[id] = session
	common.LogDebug("Registered session %s (total: %d)", id, len(s.sessions))
}

// unregisterSession removes a session from the server's session map
func (s *MoqServer) unregisterSession(id string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	delete(s.sessions, id)
	common.LogDebug("Unregistered session %s (remaining: %d)", id, len(s.sessions))
}

// Stop gracefully shuts down the MOQ server
func (s *MoqServer) Stop() error {
	common.LogInfo("Stopping MOQ server...")

	// Cancel context to stop accepting new connections
	s.cancel()

	// Close all active sessions
	s.sessionsMu.Lock()
	for id, session := range s.sessions {
		common.LogDebug("Closing session %s", id)
		if err := session.Close(); err != nil {
			logCloseError("Error closing session %s: %v", err, id)
		}
	}
	s.sessionsMu.Unlock()

	// Close the listener
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			return fmt.Errorf("failed to close listener: %w", err)
		}
	}

	common.LogInfo("MOQ server stopped")
	return nil
}

// GetSessionCount returns the number of active sessions
func (s *MoqServer) GetSessionCount() int {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	return len(s.sessions)
}
