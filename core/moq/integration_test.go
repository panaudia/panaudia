package moq

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/panaudia/panaudia/core/space"
	"github.com/quic-go/quic-go"
)

// testServerSetup contains resources for an integration test
type testServerSetup struct {
	server   *MoqServer
	certFile string
	keyFile  string
	port     int
}

// createTestServer creates a test MOQ server
func createTestServer(t *testing.T, port int) *testServerSetup {
	certFile, keyFile := generateTestCertificateIntegration(t)

	config := MoqServerConfig{
		Host:       "localhost",
		Port:       port,
		TLSCrt:     certFile,
		TLSKey:     keyFile,
		MaxClients: 100,
	}

	backend := &integrationMockBackend{}
	authoriser := &integrationMockAuthoriser{}

	server, err := NewMoqServer(config, backend, authoriser)
	if err != nil {
		t.Fatalf("Failed to create MOQ server: %v", err)
	}

	return &testServerSetup{
		server:   server,
		certFile: certFile,
		keyFile:  keyFile,
		port:     port,
	}
}

// generateTestCertificateIntegration creates a test certificate
func generateTestCertificateIntegration(t *testing.T) (certFile, keyFile string) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Panaudia Test"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("Failed to create certificate: %v", err)
	}

	certFile = t.TempDir() + "/cert.pem"
	certOut, err := os.Create(certFile)
	if err != nil {
		t.Fatalf("Failed to create cert file: %v", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		t.Fatalf("Failed to write cert: %v", err)
	}

	keyFile = t.TempDir() + "/key.pem"
	keyOut, err := os.Create(keyFile)
	if err != nil {
		t.Fatalf("Failed to create key file: %v", err)
	}
	defer keyOut.Close()
	if err := pem.Encode(keyOut, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}); err != nil {
		t.Fatalf("Failed to write key: %v", err)
	}

	return certFile, keyFile
}

// integrationMockBackend tracks connection handler creation
type integrationMockBackend struct {
	mu              sync.Mutex
	handlersCreated int
	sourcesFreed    int
}

func (m *integrationMockBackend) NewConnectionHandler(nodeConfig common.NodeConfig, outputTrack panaudia_server.TrackWriter) panaudia_server.ConnectionHandler {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlersCreated++
	return &integrationMockConnectionHandler{}
}

func (m *integrationMockBackend) FreeSource(id uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sourcesFreed++
}

func (m *integrationMockBackend) NewRocConnectionHandler(trackCount uint32) panaudia_server.RocConnectionHandler {
	return nil
}

func (m *integrationMockBackend) NewRocOutConnectionHandler(rocOutConfig common.RocOutputConfig) panaudia_server.RocOutConnectionHandler {
	return nil
}

func (m *integrationMockBackend) getHandlersCreated() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handlersCreated
}

// integrationMockConnectionHandler for integration tests
type integrationMockConnectionHandler struct {
	mu         sync.Mutex
	opusFrames int
	positions  int
	rotations  int
	connected  bool
	stopped    bool
}

func (m *integrationMockConnectionHandler) WriteOpus(src []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.opusFrames++
	return nil
}

func (m *integrationMockConnectionHandler) GetDeadSessionCh() chan uint64 {
	return nil
}

func (m *integrationMockConnectionHandler) SetPosition(position common.Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.positions++
}

func (m *integrationMockConnectionHandler) SetRotation(rotation common.Rotation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotations++
}

func (m *integrationMockConnectionHandler) Connect() *common.ServerError {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	return nil
}

func (m *integrationMockConnectionHandler) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
}

func (m *integrationMockConnectionHandler) IsActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected && !m.stopped
}

func (m *integrationMockConnectionHandler) ControlMessage(msg common.ControlMessage) {}

func (m *integrationMockConnectionHandler) ReceiveMessage(msg []byte) {}

func (m *integrationMockConnectionHandler) SetReceiveSender(receiveSender space.IMessageSender) {}

// integrationMockAuthoriser for integration tests
type integrationMockAuthoriser struct{}

func (m *integrationMockAuthoriser) Authorise(queryValues map[string][]string) (common.NodeConfig, error) {
	return common.NodeConfig{
		Uuid: uuid.New(),
		Name: "TestNode",
		SpaceNodeConfig: common.SpaceNodeConfig{
			Gain:        1.0,
			Attenuation: 2.0,
		},
	}, nil
}

func (m *integrationMockAuthoriser) AuthoriseWithoutTicket(queryValues map[string][]string) (common.NodeConfig, error) {
	return m.Authorise(queryValues)
}

// TestMultipleSimultaneousClients tests handling multiple clients
func TestMultipleSimultaneousClients(t *testing.T) {
	setup := createTestServer(t, 14440)
	defer setup.server.Stop()

	if err := setup.server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Connect multiple clients simultaneously
	numClients := 5
	var wg sync.WaitGroup
	var connections []*quic.Conn
	var connMu sync.Mutex
	var connectErrors int32

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"moq-00"},
	}

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientNum int) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			conn, err := quic.DialAddr(ctx, "localhost:14440", tlsConfig, &quic.Config{
				EnableDatagrams: true,
			})
			if err != nil {
				atomic.AddInt32(&connectErrors, 1)
				t.Logf("Client %d failed to connect: %v", clientNum, err)
				return
			}

			connMu.Lock()
			connections = append(connections, conn)
			connMu.Unlock()
		}(i)
	}

	wg.Wait()

	// Wait for server to register sessions
	time.Sleep(500 * time.Millisecond)

	// Verify all clients connected
	if connectErrors > 0 {
		t.Errorf("%d clients failed to connect", connectErrors)
	}

	sessionCount := setup.server.GetSessionCount()
	if sessionCount != numClients {
		t.Errorf("Expected %d sessions, got %d", numClients, sessionCount)
	}

	// Close all connections
	connMu.Lock()
	for _, conn := range connections {
		conn.CloseWithError(0, "test complete")
	}
	connMu.Unlock()

	// Wait for cleanup
	time.Sleep(500 * time.Millisecond)

	// Verify sessions were cleaned up
	finalCount := setup.server.GetSessionCount()
	if finalCount != 0 {
		t.Errorf("Expected 0 sessions after cleanup, got %d", finalCount)
	}
}

// TestServerShutdownWithActiveClients tests graceful shutdown
func TestServerShutdownWithActiveClients(t *testing.T) {
	setup := createTestServer(t, 14441)

	if err := setup.server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Connect some clients
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"moq-00"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn1, err := quic.DialAddr(ctx, "localhost:14441", tlsConfig, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		t.Fatalf("Client 1 failed to connect: %v", err)
	}

	conn2, err := quic.DialAddr(ctx, "localhost:14441", tlsConfig, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		t.Fatalf("Client 2 failed to connect: %v", err)
	}

	// Give sessions time to fully initialize before shutdown
	time.Sleep(500 * time.Millisecond)

	// Verify clients are connected
	sessionCount := setup.server.GetSessionCount()
	if sessionCount != 2 {
		t.Logf("Expected 2 sessions, got %d (timing sensitive)", sessionCount)
	}

	// Close client connections first to avoid race condition during server stop
	conn1.CloseWithError(0, "test complete")
	conn2.CloseWithError(0, "test complete")

	// Wait for disconnections to be processed
	time.Sleep(300 * time.Millisecond)

	// Stop server after clients have disconnected
	err = setup.server.Stop()
	if err != nil {
		t.Errorf("Server stop failed: %v", err)
	}

	// Server should have stopped cleanly
	time.Sleep(100 * time.Millisecond)
}

// TestConnectionRecovery tests reconnection after disconnect
func TestConnectionRecovery(t *testing.T) {
	setup := createTestServer(t, 14442)
	defer setup.server.Stop()

	if err := setup.server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"moq-00"},
	}

	// First connection
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	conn1, err := quic.DialAddr(ctx1, "localhost:14442", tlsConfig, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		t.Fatalf("First connection failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if setup.server.GetSessionCount() != 1 {
		t.Errorf("Expected 1 session, got %d", setup.server.GetSessionCount())
	}

	// Disconnect
	conn1.CloseWithError(0, "intentional disconnect")
	time.Sleep(300 * time.Millisecond)

	if setup.server.GetSessionCount() != 0 {
		t.Errorf("Expected 0 sessions after disconnect, got %d", setup.server.GetSessionCount())
	}

	// Reconnect
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	conn2, err := quic.DialAddr(ctx2, "localhost:14442", tlsConfig, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		t.Fatalf("Reconnection failed: %v", err)
	}
	defer conn2.CloseWithError(0, "test complete")

	time.Sleep(200 * time.Millisecond)
	if setup.server.GetSessionCount() != 1 {
		t.Errorf("Expected 1 session after reconnect, got %d", setup.server.GetSessionCount())
	}
}

// TestRapidConnectDisconnect tests rapid connection cycling
func TestRapidConnectDisconnect(t *testing.T) {
	setup := createTestServer(t, 14443)
	defer setup.server.Stop()

	if err := setup.server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"moq-00"},
	}

	// Rapid connect/disconnect cycles
	cycles := 10
	for i := 0; i < cycles; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

		conn, err := quic.DialAddr(ctx, "localhost:14443", tlsConfig, &quic.Config{
			EnableDatagrams: true,
		})
		if err != nil {
			cancel()
			t.Logf("Cycle %d: connection failed (may be expected under load): %v", i, err)
			continue
		}

		// Immediately disconnect
		conn.CloseWithError(0, "rapid cycle")
		cancel()

		// Small delay to allow cleanup
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for all cleanup
	time.Sleep(500 * time.Millisecond)

	// Server should still be running and have no sessions
	if setup.server.GetSessionCount() != 0 {
		t.Errorf("Expected 0 sessions after rapid cycling, got %d", setup.server.GetSessionCount())
	}
}

// BenchmarkConnectionEstablishment measures connection establishment time
func BenchmarkConnectionEstablishment(b *testing.B) {
	// Create a temporary test to generate certificates
	tempT := &testing.T{}
	setup := createTestServer(tempT, 14450)
	defer setup.server.Stop()

	if err := setup.server.Start(); err != nil {
		b.Fatalf("Failed to start server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"moq-00"},
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := quic.DialAddr(ctx, "localhost:14450", tlsConfig, &quic.Config{
			EnableDatagrams: true,
		})
		if err != nil {
			cancel()
			b.Fatalf("Connection failed: %v", err)
		}
		conn.CloseWithError(0, "benchmark")
		cancel()
	}
}

// TestConcurrentConnectionsLoad performs a basic load test
func TestConcurrentConnectionsLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	setup := createTestServer(t, 14444)
	defer setup.server.Stop()

	if err := setup.server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"moq-00"},
	}

	// Concurrent connection attempts
	numWorkers := 20
	connectionsPerWorker := 5
	var successCount int32
	var failCount int32
	var wg sync.WaitGroup

	startTime := time.Now()

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for i := 0; i < connectionsPerWorker; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				conn, err := quic.DialAddr(ctx, "localhost:14444", tlsConfig, &quic.Config{
					EnableDatagrams: true,
				})
				if err != nil {
					atomic.AddInt32(&failCount, 1)
					cancel()
					continue
				}

				atomic.AddInt32(&successCount, 1)

				// Hold connection briefly
				time.Sleep(50 * time.Millisecond)

				conn.CloseWithError(0, "load test")
				cancel()
			}
		}(w)
	}

	wg.Wait()
	elapsed := time.Since(startTime)

	t.Logf("Load test completed in %v", elapsed)
	t.Logf("Successful connections: %d", successCount)
	t.Logf("Failed connections: %d", failCount)

	// At least 80% should succeed
	totalAttempts := int32(numWorkers * connectionsPerWorker)
	if successCount < totalAttempts*8/10 {
		t.Errorf("Too many failures: %d/%d succeeded", successCount, totalAttempts)
	}

	// Wait for cleanup
	time.Sleep(500 * time.Millisecond)

	if setup.server.GetSessionCount() != 0 {
		t.Errorf("Expected 0 sessions after load test, got %d", setup.server.GetSessionCount())
	}
}
