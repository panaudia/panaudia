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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/panaudia/panaudia/core/space"
	"github.com/quic-go/quic-go"
)

// mockBackend is a test implementation of the Backend interface
type mockBackend struct{}

func (m *mockBackend) NewConnectionHandler(nodeConfig common.NodeConfig, outputTrack panaudia_server.TrackWriter) panaudia_server.ConnectionHandler {
	return &mockConnectionHandler{}
}

func (m *mockBackend) FreeSource(id uuid.UUID) {
	// Mock implementation - do nothing
}

func (m *mockBackend) NewRocConnectionHandler(trackCount uint32) panaudia_server.RocConnectionHandler {
	return nil
}

func (m *mockBackend) NewRocOutConnectionHandler(rocOutConfig common.RocOutputConfig) panaudia_server.RocOutConnectionHandler {
	return nil
}

// mockConnectionHandler is a test implementation of panaudia_server.ConnectionHandler
type mockConnectionHandler struct{}

func (m *mockConnectionHandler) WriteOpus(src []byte) error {
	return nil
}

func (m *mockConnectionHandler) GetDeadSessionCh() chan uint64 {
	return nil
}

func (m *mockConnectionHandler) SetPosition(position common.Position) {
	// Mock implementation - do nothing
}

func (m *mockConnectionHandler) SetRotation(rotation common.Rotation) {
	// Mock implementation - do nothing
}

func (m *mockConnectionHandler) Connect() *common.ServerError {
	return nil
}

func (m *mockConnectionHandler) Stop() {
	// Mock implementation - do nothing
}

func (m *mockConnectionHandler) IsActive() bool {
	return true
}

func (m *mockConnectionHandler) ControlMessage(msg common.ControlMessage) {
	// Mock implementation - do nothing
}

func (m *mockConnectionHandler) ReceiveMessage(msg []byte) {
	// Mock implementation - do nothing
}

func (m *mockConnectionHandler) SetReceiveSender(receiveSender space.IMessageSender) {
	// Mock implementation - do nothing
}

// mockAuthoriser is a test implementation of the Authoriser interface
type mockAuthoriser struct{}

func (m *mockAuthoriser) Authorise(queryValues map[string][]string) (common.NodeConfig, error) {
	return common.NodeConfig{}, nil
}

func (m *mockAuthoriser) AuthoriseWithoutTicket(queryValues map[string][]string) (common.NodeConfig, error) {
	return common.NodeConfig{}, nil
}

// generateTestCertificate creates a self-signed certificate for testing
func generateTestCertificate(t *testing.T) (certFile, keyFile string) {
	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	// Create certificate template
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

	// Create self-signed certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("Failed to create certificate: %v", err)
	}

	// Write certificate to temp file
	certFile = t.TempDir() + "/cert.pem"
	certOut, err := os.Create(certFile)
	if err != nil {
		t.Fatalf("Failed to create cert file: %v", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		t.Fatalf("Failed to write cert: %v", err)
	}

	// Write private key to temp file
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

// TestNewMoqServer tests basic server creation
func TestNewMoqServer(t *testing.T) {
	certFile, keyFile := generateTestCertificate(t)

	config := MoqServerConfig{
		Host:       "localhost",
		Port:       14433,
		TLSCrt:     certFile,
		TLSKey:     keyFile,
		MaxClients: 10,
	}

	backend := &mockBackend{}
	authoriser := &mockAuthoriser{}

	server, err := NewMoqServer(config, backend, authoriser)
	if err != nil {
		t.Fatalf("Failed to create MOQ server: %v", err)
	}

	if server == nil {
		t.Fatal("Server is nil")
	}

	if server.config.Port != 14433 {
		t.Errorf("Expected port 14433, got %d", server.config.Port)
	}

	if server.GetSessionCount() != 0 {
		t.Errorf("Expected 0 sessions, got %d", server.GetSessionCount())
	}

	// Verify TLS config is set
	if server.GetTLSConfig() == nil {
		t.Error("TLS config is nil")
	}

	// Verify QUIC config is set
	if server.GetQUICConfig() == nil {
		t.Error("QUIC config is nil")
	}

	// Clean up
	if err := server.Stop(); err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}
}

// TestMoqServerStartStop tests starting and stopping the server
func TestMoqServerStartStop(t *testing.T) {
	certFile, keyFile := generateTestCertificate(t)

	config := MoqServerConfig{
		Host:       "localhost",
		Port:       14434,
		TLSCrt:     certFile,
		TLSKey:     keyFile,
		MaxClients: 10,
	}

	backend := &mockBackend{}
	authoriser := &mockAuthoriser{}

	server, err := NewMoqServer(config, backend, authoriser)
	if err != nil {
		t.Fatalf("Failed to create MOQ server: %v", err)
	}

	// Start the server
	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	// Give the server a moment to start listening
	time.Sleep(100 * time.Millisecond)

	// Stop the server
	if err := server.Stop(); err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}
}

// TestMoqServerQUICConnection tests basic QUIC connection establishment
func TestMoqServerQUICConnection(t *testing.T) {
	certFile, keyFile := generateTestCertificate(t)

	config := MoqServerConfig{
		Host:       "localhost",
		Port:       14435,
		TLSCrt:     certFile,
		TLSKey:     keyFile,
		MaxClients: 10,
	}

	backend := &mockBackend{}
	authoriser := &mockAuthoriser{}

	server, err := NewMoqServer(config, backend, authoriser)
	if err != nil {
		t.Fatalf("Failed to create MOQ server: %v", err)
	}

	// Start the server
	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Stop()

	// Give the server a moment to start listening
	time.Sleep(100 * time.Millisecond)

	// Create a test client connection
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // Skip verification for test certificate
		NextProtos:         []string{"moq-00"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Attempt to connect to the server
	conn, err := quic.DialAddr(ctx, "localhost:14435", tlsConfig, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.CloseWithError(0, "test complete")

	// Give server time to register the session
	time.Sleep(200 * time.Millisecond)

	// Verify session was registered
	if server.GetSessionCount() != 1 {
		t.Errorf("Expected 1 session, got %d", server.GetSessionCount())
	}

	// Close the connection
	if err := conn.CloseWithError(0, "test complete"); err != nil {
		t.Logf("Error closing connection: %v", err)
	}

	// Give server time to unregister the session
	time.Sleep(200 * time.Millisecond)

	// Verify session was unregistered
	if server.GetSessionCount() != 0 {
		t.Errorf("Expected 0 sessions after disconnect, got %d", server.GetSessionCount())
	}
}
