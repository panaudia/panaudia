package moq

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

// ---------------------------------------------------------------------------
// Enhanced mock types for E2E tests
// ---------------------------------------------------------------------------

// e2eMockBackend stores references to every ConnectionHandler it creates so
// tests can inspect received data.
type e2eMockBackend struct {
	mu           sync.Mutex
	handlers     []*e2eMockConnectionHandler
	sourcesFreed int

	// registry, when set, makes the mock implement
	// panaudia_server.SessionRegistryProvider so the e2e tests exercise
	// the session-registry wiring (plan/history/state-cleanup phase 2).
	registry *sessions.Registry
}

func (b *e2eMockBackend) SessionRegistry() *sessions.Registry {
	return b.registry
}

// NewConnectionHandlerWithError implements the phase-3 factory: the
// backend owns session registration (mirroring DirectBackend) so the
// e2e tests can assert the transports pass their LiveSession through
// and the departure unregisters it.
func (b *e2eMockBackend) NewConnectionHandlerWithError(nodeConfig common.NodeConfig,
	outputTrack panaudia_server.TrackWriter, live sessions.LiveSession, transport string,
) (panaudia_server.ConnectionHandler, *common.ServerError) {
	h := &e2eMockConnectionHandler{
		nodeConfig:  nodeConfig,
		outputTrack: outputTrack,
		backend:     b,
	}
	if b.registry != nil {
		if live == nil {
			live = &sessions.FuncSession{}
		}
		_, h.entry = b.registry.Register(nodeConfig.Uuid, live, transport)
	}
	b.mu.Lock()
	b.handlers = append(b.handlers, h)
	b.mu.Unlock()
	return h, nil
}

func (b *e2eMockBackend) NewConnectionHandler(nodeConfig common.NodeConfig, outputTrack panaudia_server.TrackWriter) panaudia_server.ConnectionHandler {
	h := &e2eMockConnectionHandler{
		nodeConfig:  nodeConfig,
		outputTrack: outputTrack,
		backend:     b,
	}
	b.mu.Lock()
	b.handlers = append(b.handlers, h)
	b.mu.Unlock()
	return h
}

func (b *e2eMockBackend) FreeSource(_ uuid.UUID) {
	b.mu.Lock()
	b.sourcesFreed++
	b.mu.Unlock()
}

func (b *e2eMockBackend) NewRocConnectionHandler(_ uint32) panaudia_server.RocConnectionHandler {
	return nil
}

func (b *e2eMockBackend) NewRocOutConnectionHandler(_ common.RocOutputConfig) panaudia_server.RocOutConnectionHandler {
	return nil
}

func (b *e2eMockBackend) getHandlers() []*e2eMockConnectionHandler {
	b.mu.Lock()
	defer b.mu.Unlock()
	dst := make([]*e2eMockConnectionHandler, len(b.handlers))
	copy(dst, b.handlers)
	return dst
}

func (b *e2eMockBackend) getSourcesFreed() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sourcesFreed
}

func (b *e2eMockBackend) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = nil
	b.sourcesFreed = 0
}

// e2eMockConnectionHandler records all calls so tests can verify them.
type e2eMockConnectionHandler struct {
	mu          sync.Mutex
	nodeConfig  common.NodeConfig
	outputTrack panaudia_server.TrackWriter
	opusFrames  [][]byte
	positions   []common.Position
	rotations   []common.Rotation
	connected   bool
	stopped     bool
	// backend is the parent mock so Stop() can invoke FreeSource —
	// mirrors the real direct backend's path where ConnectionHandler.Stop
	// → ISpace.DeleteNode → mixer-tick processChanges → backend.FreeSource.
	// Without this the test mock counts zero FreeSource calls and tests
	// asserting on `sourcesFreed` time out.
	backend *e2eMockBackend

	// entry is the mock's registry entry (set by the factory when the
	// backend has a registry); Stop releases it like DepartNode would.
	entry *sessions.Entry
}

func (h *e2eMockConnectionHandler) WriteOpus(src []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cpy := make([]byte, len(src))
	copy(cpy, src)
	h.opusFrames = append(h.opusFrames, cpy)
	return nil
}

func (h *e2eMockConnectionHandler) GetDeadSessionCh() chan uint64 { return nil }

func (h *e2eMockConnectionHandler) SetPosition(p common.Position) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.positions = append(h.positions, p)
}

func (h *e2eMockConnectionHandler) SetRotation(r common.Rotation) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rotations = append(h.rotations, r)
}

func (h *e2eMockConnectionHandler) Connect() *common.ServerError {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connected = true
	return nil
}

func (h *e2eMockConnectionHandler) Stop() {
	h.mu.Lock()
	h.stopped = true
	backend := h.backend
	uuid := h.nodeConfig.Uuid
	entry := h.entry
	h.mu.Unlock()
	if backend != nil {
		backend.FreeSource(uuid)
		if backend.registry != nil && entry != nil {
			backend.registry.Unregister(entry)
			entry.MarkDeparted()
		}
	}
}

func (h *e2eMockConnectionHandler) IsActive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connected && !h.stopped
}

func (h *e2eMockConnectionHandler) ControlMessage(_ common.ControlMessage)  {}
func (h *e2eMockConnectionHandler) ReceiveMessage(_ []byte)                 {}
func (h *e2eMockConnectionHandler) SetReceiveSender(_ space.IMessageSender) {}

func (h *e2eMockConnectionHandler) getOpusFrames() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	dst := make([][]byte, len(h.opusFrames))
	copy(dst, h.opusFrames)
	return dst
}

func (h *e2eMockConnectionHandler) getPositions() []common.Position {
	h.mu.Lock()
	defer h.mu.Unlock()
	dst := make([]common.Position, len(h.positions))
	copy(dst, h.positions)
	return dst
}

func (h *e2eMockConnectionHandler) getRotations() []common.Rotation {
	h.mu.Lock()
	defer h.mu.Unlock()
	dst := make([]common.Rotation, len(h.rotations))
	copy(dst, h.rotations)
	return dst
}

// switchableAuthoriser delegates to whatever authoriser is currently set.
// This lets subtests reconfigure auth behaviour without creating a new server.
type switchableAuthoriser struct {
	mu       sync.Mutex
	delegate Authoriser
}

func (a *switchableAuthoriser) Authorise(q map[string][]string) (common.NodeConfig, error) {
	a.mu.Lock()
	d := a.delegate
	a.mu.Unlock()
	return d.Authorise(q)
}

func (a *switchableAuthoriser) AuthoriseWithoutTicket(q map[string][]string) (common.NodeConfig, error) {
	a.mu.Lock()
	d := a.delegate
	a.mu.Unlock()
	return d.AuthoriseWithoutTicket(q)
}

func (a *switchableAuthoriser) setDelegate(d Authoriser) {
	a.mu.Lock()
	a.delegate = d
	a.mu.Unlock()
}

// acceptAuthoriser always accepts and returns a fixed NodeConfig.
type acceptAuthoriser struct {
	nodeConfig common.NodeConfig
}

func (a *acceptAuthoriser) Authorise(_ map[string][]string) (common.NodeConfig, error) {
	return a.nodeConfig, nil
}

func (a *acceptAuthoriser) AuthoriseWithoutTicket(_ map[string][]string) (common.NodeConfig, error) {
	return a.nodeConfig, nil
}

// rejectAuthoriser always rejects.
type rejectAuthoriser struct{}

func (*rejectAuthoriser) Authorise(_ map[string][]string) (common.NodeConfig, error) {
	return common.NodeConfig{}, fmt.Errorf("unauthorized")
}

func (*rejectAuthoriser) AuthoriseWithoutTicket(_ map[string][]string) (common.NodeConfig, error) {
	return common.NodeConfig{}, fmt.Errorf("unauthorized")
}

// roundRobinAuthoriser returns a different NodeConfig for each call.
type roundRobinAuthoriser struct {
	mu      sync.Mutex
	configs []common.NodeConfig
	idx     int
}

func (a *roundRobinAuthoriser) Authorise(_ map[string][]string) (common.NodeConfig, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.idx >= len(a.configs) {
		return common.NodeConfig{}, fmt.Errorf("no more configs")
	}
	cfg := a.configs[a.idx]
	a.idx++
	return cfg, nil
}

func (a *roundRobinAuthoriser) AuthoriseWithoutTicket(q map[string][]string) (common.NodeConfig, error) {
	return a.Authorise(q)
}

// ---------------------------------------------------------------------------
// E2E test client – a pure-Go MOQ client that mirrors the TS browser client
// ---------------------------------------------------------------------------

// clientPublisher is what we receive when the server subscribes to one of our tracks.
type clientPublisher struct {
	namespace []string
	publisher moqtransport.Publisher
}

// e2eTestClient wraps a Go-side moqtransport.Session talking to the server.
type e2eTestClient struct {
	t          *testing.T
	conn       *quic.Conn
	moqSession *moqtransport.Session

	// publisherCh receives publishers when the server SUBSCRIBEs to our announced tracks.
	publisherCh chan clientPublisher

	// outputTrack is the RemoteTrack we get when we subscribe to the server's output.
	outputTrack *moqtransport.RemoteTrack
}

// clientHandler handles server-initiated ANNOUNCE / SUBSCRIBE_ANNOUNCES on the client.
type clientHandler struct{}

func (clientHandler) Handle(rw moqtransport.ResponseWriter, msg *moqtransport.Message) {
	if msg == nil {
		return
	}
	switch msg.Method {
	case moqtransport.MessageAnnounce,
		moqtransport.MessageSubscribeAnnounces:
		_ = rw.Accept()
	default:
		// Ignore other messages
	}
}

// clientSubscribeHandler handles server-initiated SUBSCRIBEs to our input tracks.
type clientSubscribeHandler struct {
	ch chan clientPublisher
}

func (h *clientSubscribeHandler) HandleSubscribe(w *moqtransport.SubscribeResponseWriter, msg *moqtransport.SubscribeMessage) {
	if err := w.Accept(); err != nil {
		return
	}
	h.ch <- clientPublisher{
		namespace: msg.Namespace,
		publisher: w,
	}
}

// dialE2EClient creates a new test client connected to the server over the
// default draft-14 ALPN ("moq-00").
func dialE2EClient(t *testing.T, port int) *e2eTestClient {
	return dialE2EClientALPN(t, port, "moq-00")
}

// dialE2EClientALPN creates a test client negotiating a specific ALPN, e.g.
// "moqt-16" for draft-16 or "moq-00" for draft-14.
func dialE2EClientALPN(t *testing.T, port int, alpn string) *e2eTestClient {
	t.Helper()

	tlsCfg := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{alpn},
	}
	quicCfg := &quic.Config{
		EnableDatagrams:       true,
		MaxIncomingStreams:    100,
		MaxIncomingUniStreams: 100,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := quic.DialAddr(ctx, fmt.Sprintf("localhost:%d", port), tlsCfg, quicCfg)
	if err != nil {
		t.Fatalf("quic.DialAddr: %v", err)
	}

	pubCh := make(chan clientPublisher, 10)

	session := &moqtransport.Session{
		Handler:             clientHandler{},
		SubscribeHandler:    &clientSubscribeHandler{ch: pubCh},
		InitialMaxRequestID: 100,
	}

	// Run handshake (CLIENT_SETUP / SERVER_SETUP)
	if err := session.Run(quicmoq.NewClient(conn)); err != nil {
		conn.CloseWithError(0, "handshake failed")
		t.Fatalf("session.Run: %v", err)
	}

	return &e2eTestClient{
		t:           t,
		conn:        conn,
		moqSession:  session,
		publisherCh: pubCh,
	}
}

// subscribe subscribes to the output track with a JWT token (triggers auth).
func (c *e2eTestClient) subscribe(ctx context.Context, namespace []string, token string) {
	c.t.Helper()
	track, err := c.moqSession.Subscribe(ctx, namespace, "", moqtransport.WithAuthorizationToken(token))
	if err != nil {
		c.t.Fatalf("Subscribe: %v", err)
	}
	c.outputTrack = track
}

// announce tells the server we will publish on the given namespace.
func (c *e2eTestClient) announce(ctx context.Context, namespace []string) {
	c.t.Helper()
	if err := c.moqSession.Announce(ctx, namespace); err != nil {
		c.t.Fatalf("Announce(%v): %v", namespace, err)
	}
}

// waitPublisher blocks until the server subscribes to one of our announced tracks
// and returns the publisher we can use to send datagrams.
func (c *e2eTestClient) waitPublisher(ctx context.Context) clientPublisher {
	c.t.Helper()
	select {
	case pub := <-c.publisherCh:
		return pub
	case <-ctx.Done():
		c.t.Fatalf("timeout waiting for server SUBSCRIBE")
		return clientPublisher{}
	}
}

// close shuts down the client gracefully.
func (c *e2eTestClient) close() {
	if c.outputTrack != nil {
		_ = c.outputTrack.Close()
	}
	_ = c.moqSession.Close()
	_ = (*c.conn).CloseWithError(0, "test done")
}

// ---------------------------------------------------------------------------
// Helper: wait with timeout for a condition to become true
// ---------------------------------------------------------------------------

func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", desc)
}

// ---------------------------------------------------------------------------
// TestE2E is the parent test – it creates a single server, then runs subtests.
// We must use one server because MoqServer.Start() registers on the global
// http.DefaultServeMux, which panics on duplicate patterns.
// ---------------------------------------------------------------------------

// TestE2EDraft16 proves the server negotiates and runs a full draft-16 session.
// Dialing with ALPN "moqt-16" exercises Eyevinn's ALPN-based version selection,
// the delta-encoded SETUP/SUBSCRIBE wire format, and the AuthorizationToken
// (Token struct) round-trip — end to end through our MoqServer.
func TestE2EDraft16(t *testing.T) {
	certFile, keyFile := generateTestCertificateIntegration(t)

	backend := &e2eMockBackend{registry: sessions.NewRegistry()}
	authoriser := &switchableAuthoriser{}

	const port = 15510

	config := MoqServerConfig{
		Host:       "localhost",
		Port:       port,
		TLSCrt:     certFile,
		TLSKey:     keyFile,
		MaxClients: 100,
	}

	server, err := NewMoqServer(config, backend, authoriser)
	if err != nil {
		t.Fatalf("NewMoqServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	time.Sleep(100 * time.Millisecond)

	nodeConfig := common.NodeConfig{
		Uuid: uuid.New(),
		Name: "Draft16Node",
		SpaceNodeConfig: common.SpaceNodeConfig{
			Gain: 1.0, Attenuation: 2.0,
		},
	}
	authoriser.setDelegate(&acceptAuthoriser{nodeConfig: nodeConfig})

	client := dialE2EClientALPN(t, port, "moqt-16")
	defer client.close()

	// The QUIC handshake must have selected moqt-16 — proving our server offers
	// it and the client negotiated draft-16 (not the draft-14 "moq-00" fallback).
	if got := client.conn.ConnectionState().TLS.NegotiatedProtocol; got != "moqt-16" {
		t.Fatalf("negotiated ALPN = %q, want \"moqt-16\"", got)
	}

	// Full subscribe + auth flow over draft-16: this only succeeds if the
	// delta-encoded CLIENT_SETUP / SUBSCRIBE and the AuthorizationToken Token
	// struct all parse correctly on the server.
	trackNames := GenerateTrackNames(nodeConfig)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client.subscribe(ctx, trackNames.AudioOutputNamespace, "test-jwt-token")

	waitFor(t, 5*time.Second, "backend handler created (draft-16)", func() bool {
		return len(backend.getHandlers()) > 0
	})
	handlers := backend.getHandlers()
	if len(handlers) != 1 {
		t.Fatalf("expected 1 handler, got %d", len(handlers))
	}
	waitFor(t, 3*time.Second, "handler connected (draft-16)", func() bool {
		handlers[0].mu.Lock()
		defer handlers[0].mu.Unlock()
		return handlers[0].connected
	})

	// Session registry (phase 2): the admitted session is registered,
	// live, and on the right transport…
	entry := backend.registry.Get(nodeConfig.Uuid)
	if entry == nil {
		t.Fatal("admitted session not in the session registry")
	}
	if entry.Transport != "moq-quic" {
		t.Errorf("registry transport = %q, want moq-quic", entry.Transport)
	}
	if !entry.Session.Alive() {
		t.Error("registered session reports !Alive while connected")
	}

	// …and unregistered (entry departed) once the client disconnects.
	client.close()
	waitFor(t, 5*time.Second, "session unregistered after disconnect", func() bool {
		return backend.registry.Get(nodeConfig.Uuid) == nil
	})
	select {
	case <-entry.Departed():
	default:
		t.Error("entry not marked departed after disconnect")
	}
}

// TestE2EDraft16WebTransport proves draft-16 works over WebTransport, the path
// the browser uses. The webtransport Dialer offers "moqt-16" via
// WT-Available-Protocols (exactly as the browser's WebTransport `protocols`
// option does); the server must echo it so Eyevinn selects draft-16 from
// SessionState().ApplicationProtocol. Without the server's ApplicationProtocols
// config this would silently fall back to draft-14.
func TestE2EDraft16WebTransport(t *testing.T) {
	certFile, keyFile := generateTestCertificateIntegration(t)

	backend := &e2eMockBackend{}
	authoriser := &switchableAuthoriser{}

	const port = 15511

	config := MoqServerConfig{
		Host:       "localhost",
		Port:       port,
		TLSCrt:     certFile,
		TLSKey:     keyFile,
		MaxClients: 100,
	}
	server, err := NewMoqServer(config, backend, authoriser)
	if err != nil {
		t.Fatalf("NewMoqServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	time.Sleep(100 * time.Millisecond)

	nodeConfig := common.NodeConfig{
		Uuid: uuid.New(),
		Name: "WTDraft16Node",
		SpaceNodeConfig: common.SpaceNodeConfig{
			Gain: 1.0, Attenuation: 2.0,
		},
	}
	authoriser.setDelegate(&acceptAuthoriser{nodeConfig: nodeConfig})

	dialer := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}},
		QUICConfig: &quic.Config{
			EnableDatagrams:                  true,
			MaxIncomingStreams:               100,
			MaxIncomingUniStreams:            100,
			EnableStreamResetPartialDelivery: true, // required by webtransport-go v0.10
		},
		ApplicationProtocols: []string{"moqt-16"},
	}
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()
	rsp, wtSession, err := dialer.Dial(dialCtx, fmt.Sprintf("https://localhost:%d/moq", port), nil)
	if err != nil {
		t.Fatalf("webtransport Dial: %v", err)
	}
	defer wtSession.CloseWithError(0, "test done")

	// The browser-equivalent subprotocol negotiation must have selected moqt-16.
	if got := wtSession.SessionState().ApplicationProtocol; got != "moqt-16" {
		t.Fatalf("negotiated WT subprotocol = %q, want \"moqt-16\" (WT-Protocol header=%q)",
			got, rsp.Header.Get("WT-Protocol"))
	}

	// Run a full draft-16 MoQ session over WebTransport.
	pubCh := make(chan clientPublisher, 10)
	session := &moqtransport.Session{
		Handler:             clientHandler{},
		SubscribeHandler:    &clientSubscribeHandler{ch: pubCh},
		InitialMaxRequestID: 100,
	}
	if err := session.Run(webtransportmoq.NewClient(wtSession)); err != nil {
		t.Fatalf("session.Run over WebTransport: %v", err)
	}
	defer func() { _ = session.Close() }()

	trackNames := GenerateTrackNames(nodeConfig)
	subCtx, subCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer subCancel()
	if _, err := session.Subscribe(subCtx, trackNames.AudioOutputNamespace, "",
		moqtransport.WithAuthorizationToken("test-jwt-token")); err != nil {
		t.Fatalf("Subscribe over WebTransport draft-16: %v", err)
	}

	waitFor(t, 5*time.Second, "backend handler created (WT draft-16)", func() bool {
		return len(backend.getHandlers()) > 0
	})
}

func TestE2E(t *testing.T) {
	certFile, keyFile := generateTestCertificateIntegration(t)

	backend := &e2eMockBackend{}
	authoriser := &switchableAuthoriser{}

	const port = 15500

	config := MoqServerConfig{
		Host:       "localhost",
		Port:       port,
		TLSCrt:     certFile,
		TLSKey:     keyFile,
		MaxClients: 100,
	}

	server, err := NewMoqServer(config, backend, authoriser)
	if err != nil {
		t.Fatalf("NewMoqServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	time.Sleep(100 * time.Millisecond)

	// Helper to wait for all sessions to drain between subtests.
	drainSessions := func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if server.GetSessionCount() == 0 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// -----------------------------------------------------------------------
	// Subtest 1: Handshake + Auth
	// -----------------------------------------------------------------------
	t.Run("HandshakeAndAuth", func(t *testing.T) {
		nodeConfig := common.NodeConfig{
			Uuid: uuid.New(),
			Name: "HandshakeNode",
			SpaceNodeConfig: common.SpaceNodeConfig{
				Gain: 1.0, Attenuation: 2.0,
			},
		}
		backend.reset()
		authoriser.setDelegate(&acceptAuthoriser{nodeConfig: nodeConfig})

		client := dialE2EClient(t, port)
		defer client.close()

		trackNames := GenerateTrackNames(nodeConfig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		client.subscribe(ctx, trackNames.AudioOutputNamespace, "test-jwt-token")

		// Verify backend created a ConnectionHandler
		waitFor(t, 5*time.Second, "backend handler created", func() bool {
			return len(backend.getHandlers()) > 0
		})

		handlers := backend.getHandlers()
		if len(handlers) != 1 {
			t.Fatalf("expected 1 handler, got %d", len(handlers))
		}

		// Verify Connect() was called
		waitFor(t, 3*time.Second, "handler connected", func() bool {
			handlers[0].mu.Lock()
			defer handlers[0].mu.Unlock()
			return handlers[0].connected
		})

		client.close()
		drainSessions()
	})

	// -----------------------------------------------------------------------
	// Subtest 2: Audio input flow
	// -----------------------------------------------------------------------
	t.Run("AudioInputFlow", func(t *testing.T) {
		nodeConfig := common.NodeConfig{
			Uuid: uuid.New(),
			Name: "AudioInputNode",
			SpaceNodeConfig: common.SpaceNodeConfig{
				Gain: 1.0, Attenuation: 2.0,
			},
		}
		backend.reset()
		authoriser.setDelegate(&acceptAuthoriser{nodeConfig: nodeConfig})
		trackNames := GenerateTrackNames(nodeConfig)

		client := dialE2EClient(t, port)
		defer client.close()

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// Subscribe to output (triggers auth + server-side setup)
		client.subscribe(ctx, trackNames.AudioOutputNamespace, "test-jwt")

		waitFor(t, 5*time.Second, "backend handler created", func() bool {
			return len(backend.getHandlers()) > 0
		})

		// Announce audio input track
		client.announce(ctx, trackNames.AudioInputNamespace)

		// Wait for server to subscribe to our audio input
		audioPub := client.waitPublisher(ctx)
		t.Logf("Server subscribed to: %v", audioPub.namespace)

		// Publish Opus datagrams
		numFrames := 10
		for i := 0; i < numFrames; i++ {
			frame := make([]byte, 80)
			for j := range frame {
				frame[j] = byte(i + j)
			}
			obj := moqtransport.Object{
				GroupID:  uint64(time.Now().UnixMilli()),
				ObjectID: uint64(i),
				Payload:  frame,
			}
			if err := audioPub.publisher.SendDatagram(obj); err != nil {
				t.Fatalf("SendDatagram frame %d: %v", i, err)
			}
			time.Sleep(5 * time.Millisecond)
		}

		// Verify mock handler received the frames
		handler := backend.getHandlers()[0]
		waitFor(t, 5*time.Second, fmt.Sprintf("%d opus frames received", numFrames), func() bool {
			return len(handler.getOpusFrames()) >= numFrames
		})

		received := handler.getOpusFrames()
		for i := 0; i < numFrames; i++ {
			if len(received[i]) != 80 {
				t.Errorf("frame %d: expected 80 bytes, got %d", i, len(received[i]))
			}
			if received[i][0] != byte(i) {
				t.Errorf("frame %d: first byte expected %d, got %d", i, i, received[i][0])
			}
		}

		client.close()
		drainSessions()
	})

	// -----------------------------------------------------------------------
	// Subtest 3: State input flow
	// -----------------------------------------------------------------------
	t.Run("StateInputFlow", func(t *testing.T) {
		nodeConfig := common.NodeConfig{
			Uuid: uuid.New(),
			Name: "StateInputNode",
			SpaceNodeConfig: common.SpaceNodeConfig{
				Gain: 1.0, Attenuation: 2.0,
			},
		}
		backend.reset()
		authoriser.setDelegate(&acceptAuthoriser{nodeConfig: nodeConfig})
		trackNames := GenerateTrackNames(nodeConfig)

		client := dialE2EClient(t, port)
		defer client.close()

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		client.subscribe(ctx, trackNames.AudioOutputNamespace, "test-jwt")

		waitFor(t, 5*time.Second, "backend handler created", func() bool {
			return len(backend.getHandlers()) > 0
		})

		// Announce state track
		client.announce(ctx, trackNames.StateInputNamespace)

		// Server subscribes to audio + state + control in that order; skip
		// past the audio publisher to get the state one (same pattern used
		// by FullRoundTrip).
		_ = client.waitPublisher(ctx) // audio input
		statePub := client.waitPublisher(ctx)
		t.Logf("Server subscribed to: %v", statePub.namespace)

		// Publish NodeInfo3 state datagrams
		expectedPos := common.Position{X: 0.3, Y: 0.5, Z: 0.7}
		expectedRot := common.Rotation{Yaw: 45.0, Pitch: 10.0, Roll: 5.0}

		payload := inout.NodeInfo3ToBytes(common.NodeInfo3{
			Uuid:     nodeConfig.Uuid,
			Position: expectedPos,
			Rotation: expectedRot,
			Volume:   1.0,
		})

		numUpdates := 5
		for i := 0; i < numUpdates; i++ {
			obj := moqtransport.Object{
				GroupID:  uint64(time.Now().UnixMilli()),
				ObjectID: uint64(i),
				Payload:  payload,
			}
			if err := statePub.publisher.SendDatagram(obj); err != nil {
				t.Fatalf("SendDatagram state %d: %v", i, err)
			}
			time.Sleep(5 * time.Millisecond)
		}

		handler := backend.getHandlers()[0]
		waitFor(t, 5*time.Second, fmt.Sprintf("%d positions received", numUpdates), func() bool {
			return len(handler.getPositions()) >= numUpdates
		})

		positions := handler.getPositions()
		rotations := handler.getRotations()

		if len(rotations) < numUpdates {
			t.Fatalf("expected >=%d rotations, got %d", numUpdates, len(rotations))
		}

		const epsilon = 0.001
		p := positions[0]
		if diff := p.X - expectedPos.X; diff > epsilon || diff < -epsilon {
			t.Errorf("position X: expected ~%.3f, got %.3f", expectedPos.X, p.X)
		}
		if diff := p.Y - expectedPos.Y; diff > epsilon || diff < -epsilon {
			t.Errorf("position Y: expected ~%.3f, got %.3f", expectedPos.Y, p.Y)
		}
		if diff := p.Z - expectedPos.Z; diff > epsilon || diff < -epsilon {
			t.Errorf("position Z: expected ~%.3f, got %.3f", expectedPos.Z, p.Z)
		}

		r := rotations[0]
		if diff := r.Yaw - expectedRot.Yaw; diff > epsilon || diff < -epsilon {
			t.Errorf("rotation Yaw: expected ~%.3f, got %.3f", expectedRot.Yaw, r.Yaw)
		}

		client.close()
		drainSessions()
	})

	// -----------------------------------------------------------------------
	// Subtest 4: Audio output flow
	// -----------------------------------------------------------------------
	t.Run("AudioOutputFlow", func(t *testing.T) {
		nodeConfig := common.NodeConfig{
			Uuid: uuid.New(),
			Name: "AudioOutputNode",
			SpaceNodeConfig: common.SpaceNodeConfig{
				Gain: 1.0, Attenuation: 2.0,
			},
		}
		backend.reset()
		authoriser.setDelegate(&acceptAuthoriser{nodeConfig: nodeConfig})
		trackNames := GenerateTrackNames(nodeConfig)

		client := dialE2EClient(t, port)
		defer client.close()

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// Subscribe to output (triggers auth, gets RemoteTrack)
		client.subscribe(ctx, trackNames.AudioOutputNamespace, "test-jwt")

		waitFor(t, 5*time.Second, "backend handler created", func() bool {
			return len(backend.getHandlers()) > 0
		})

		handler := backend.getHandlers()[0]

		// Give the publisher time to be wired up
		time.Sleep(500 * time.Millisecond)

		// Write samples via the mock handler's outputTrack (MoqTrackAdapter)
		numSamples := 5
		for i := 0; i < numSamples; i++ {
			frame := make([]byte, 80)
			for j := range frame {
				frame[j] = byte(100 + i + j)
			}
			if err := handler.outputTrack.WriteSample(media.Sample{Data: frame}); err != nil {
				t.Logf("WriteSample %d: %v", i, err)
			}
			time.Sleep(5 * time.Millisecond)
		}

		// Read from client's RemoteTrack
		received := 0
		readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
		defer readCancel()

		for received < numSamples {
			obj, err := client.outputTrack.ReadObject(readCtx)
			if err != nil {
				t.Logf("ReadObject: %v (received %d/%d)", err, received, numSamples)
				break
			}
			if len(obj.Payload) != 80 {
				t.Errorf("output frame %d: expected 80 bytes, got %d", received, len(obj.Payload))
			}
			received++
		}

		if received == 0 {
			t.Errorf("received 0 output audio frames (expected %d)", numSamples)
		} else {
			t.Logf("Received %d/%d output audio frames", received, numSamples)
		}

		client.close()
		drainSessions()
	})

	// -----------------------------------------------------------------------
	// Subtest 5: Full round-trip with two clients
	// -----------------------------------------------------------------------
	t.Run("FullRoundTrip", func(t *testing.T) {
		nodeA := common.NodeConfig{
			Uuid: uuid.New(),
			Name: "ClientA",
			SpaceNodeConfig: common.SpaceNodeConfig{
				Gain: 1.0, Attenuation: 2.0,
			},
		}
		nodeB := common.NodeConfig{
			Uuid: uuid.New(),
			Name: "ClientB",
			SpaceNodeConfig: common.SpaceNodeConfig{
				Gain: 1.0, Attenuation: 2.0,
			},
		}

		backend.reset()
		authoriser.setDelegate(&roundRobinAuthoriser{
			configs: []common.NodeConfig{nodeA, nodeB},
		})

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		// --- Client A ---
		clientA := dialE2EClient(t, port)
		defer clientA.close()
		trackNamesA := GenerateTrackNames(nodeA)
		clientA.subscribe(ctx, trackNamesA.AudioOutputNamespace, "jwt-a")

		waitFor(t, 5*time.Second, "handler A created", func() bool {
			return len(backend.getHandlers()) >= 1
		})

		clientA.announce(ctx, trackNamesA.AudioInputNamespace)
		audioPubA := clientA.waitPublisher(ctx)

		// --- Client B ---
		clientB := dialE2EClient(t, port)
		defer clientB.close()
		trackNamesB := GenerateTrackNames(nodeB)
		clientB.subscribe(ctx, trackNamesB.AudioOutputNamespace, "jwt-b")

		waitFor(t, 5*time.Second, "handler B created", func() bool {
			return len(backend.getHandlers()) >= 2
		})

		clientB.announce(ctx, trackNamesB.AudioInputNamespace)
		audioPubB := clientB.waitPublisher(ctx)

		// Both announce state tracks
		clientA.announce(ctx, trackNamesA.StateInputNamespace)
		statePubA := clientA.waitPublisher(ctx)

		clientB.announce(ctx, trackNamesB.StateInputNamespace)
		statePubB := clientB.waitPublisher(ctx)

		// Client A publishes audio
		for i := 0; i < 5; i++ {
			frame := make([]byte, 80)
			frame[0] = byte(i)
			_ = audioPubA.publisher.SendDatagram(moqtransport.Object{
				GroupID: uint64(time.Now().UnixMilli()), ObjectID: uint64(i), Payload: frame,
			})
			time.Sleep(5 * time.Millisecond)
		}

		// Client B publishes audio
		for i := 0; i < 5; i++ {
			frame := make([]byte, 80)
			frame[0] = byte(50 + i)
			_ = audioPubB.publisher.SendDatagram(moqtransport.Object{
				GroupID: uint64(time.Now().UnixMilli()), ObjectID: uint64(i), Payload: frame,
			})
			time.Sleep(5 * time.Millisecond)
		}

		// Both publish state
		_ = statePubA.publisher.SendDatagram(moqtransport.Object{
			GroupID: uint64(time.Now().UnixMilli()),
			Payload: inout.NodeInfo3ToBytes(common.NodeInfo3{
				Uuid:     nodeA.Uuid,
				Position: common.Position{X: 0.1, Y: 0.2, Z: 0.3},
				Rotation: common.Rotation{Yaw: 10, Pitch: 20, Roll: 30},
				Volume:   1.0,
			}),
		})

		_ = statePubB.publisher.SendDatagram(moqtransport.Object{
			GroupID: uint64(time.Now().UnixMilli()),
			Payload: inout.NodeInfo3ToBytes(common.NodeInfo3{
				Uuid:     nodeB.Uuid,
				Position: common.Position{X: 0.7, Y: 0.8, Z: 0.9},
				Rotation: common.Rotation{Yaw: 90, Pitch: 0, Roll: 0},
				Volume:   0.5,
			}),
		})

		// Verify handler A received audio
		handlerA := backend.getHandlers()[0]
		waitFor(t, 5*time.Second, "handler A received audio", func() bool {
			return len(handlerA.getOpusFrames()) >= 5
		})

		// Verify handler B received audio
		handlerB := backend.getHandlers()[1]
		waitFor(t, 5*time.Second, "handler B received audio", func() bool {
			return len(handlerB.getOpusFrames()) >= 5
		})

		// Verify state updates arrived
		waitFor(t, 5*time.Second, "handler A received state", func() bool {
			return len(handlerA.getPositions()) >= 1
		})
		waitFor(t, 5*time.Second, "handler B received state", func() bool {
			return len(handlerB.getPositions()) >= 1
		})

		t.Logf("Client A: %d audio frames, %d positions",
			len(handlerA.getOpusFrames()), len(handlerA.getPositions()))
		t.Logf("Client B: %d audio frames, %d positions",
			len(handlerB.getOpusFrames()), len(handlerB.getPositions()))

		// Disconnect both
		clientA.close()
		clientB.close()

		// Verify cleanup
		waitFor(t, 5*time.Second, "all sessions cleaned up", func() bool {
			return server.GetSessionCount() == 0
		})
		waitFor(t, 3*time.Second, "sources freed", func() bool {
			return backend.getSourcesFreed() >= 2
		})
	})

	// -----------------------------------------------------------------------
	// Subtest 6: Auth failure
	// -----------------------------------------------------------------------
	t.Run("AuthFailure", func(t *testing.T) {
		nodeConfig := common.NodeConfig{
			Uuid: uuid.New(),
			Name: "RejectNode",
			SpaceNodeConfig: common.SpaceNodeConfig{
				Gain: 1.0, Attenuation: 2.0,
			},
		}
		backend.reset()
		authoriser.setDelegate(&rejectAuthoriser{})

		client := dialE2EClient(t, port)
		defer client.close()

		trackNames := GenerateTrackNames(nodeConfig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := client.moqSession.Subscribe(
			ctx,
			trackNames.AudioOutputNamespace,
			"",
			moqtransport.WithAuthorizationToken("bad-token"),
		)

		if err == nil {
			t.Fatal("expected subscribe to fail with auth error, but it succeeded")
		}
		t.Logf("Subscribe correctly rejected: %v", err)

		// Verify no ConnectionHandler was created
		if handlers := backend.getHandlers(); len(handlers) != 0 {
			t.Errorf("expected 0 handlers after auth failure, got %d", len(handlers))
		}

		client.close()
		drainSessions()
	})
}
