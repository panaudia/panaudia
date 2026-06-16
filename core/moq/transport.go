package moq

import (
	"crypto/tls"
	"fmt"
	"os"
	"time"

	"github.com/panaudia/panaudia/core/common"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
)

// initTLS initializes the TLS configuration for QUIC
func (s *MoqServer) initTLS() error {
	// Load TLS certificate and key
	cert, err := tls.LoadX509KeyPair(s.config.TLSCrt, s.config.TLSKey)
	if err != nil {
		return fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	// Create TLS config
	// WebTransport uses HTTP/3 which requires "h3" ALPN.
	// "moqt-16" selects draft-16 for raw-QUIC MOQ clients (ALPN-negotiated);
	// "moq-00" is retained for draft-14 in-band negotiation / older clients.
	s.tlsConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h3", "moqt-16", "moq-00"},
		MinVersion:   tls.VersionTLS13, // QUIC requires TLS 1.3
	}

	// Log certificate info for debugging
	if _, err := os.Stat(s.config.TLSCrt); err == nil {
		common.LogDebug("Loaded TLS certificate from %s", s.config.TLSCrt)
	}
	if _, err := os.Stat(s.config.TLSKey); err == nil {
		common.LogDebug("Loaded TLS key from %s", s.config.TLSKey)
	}

	return nil
}

// initQUIC initializes the QUIC configuration
func (s *MoqServer) initQUIC() {
	s.quicConfig = &quic.Config{
		// Maximum number of incoming streams per connection
		MaxIncomingStreams: 100,

		// Maximum number of incoming unidirectional streams
		MaxIncomingUniStreams: 100,

		// Idle timeout - close connection if no activity
		MaxIdleTimeout: 5 * time.Second,

		// Keep alive interval
		KeepAlivePeriod: 2 * time.Second,

		// Enable datagram support (may be needed for MOQ)
		EnableDatagrams: true,

		// Required by webtransport-go v0.10 (quic-go v0.59) for WebTransport
		// sessions — the browser path fails the handshake without it.
		EnableStreamResetPartialDelivery: true,

		// Maximum receive stream flow control window
		InitialStreamReceiveWindow: 512 * 1024,      // 512 KB
		MaxStreamReceiveWindow:     6 * 1024 * 1024, // 6 MB

		// Maximum receive connection flow control window
		InitialConnectionReceiveWindow: 1024 * 1024,      // 1 MB
		MaxConnectionReceiveWindow:     15 * 1024 * 1024, // 15 MB

		// Handshake diagnostics: set QLOGDIR to write per-connection qlog
		// traces (SETTINGS frames, stream events, close reasons) — used to
		// debug browser WebTransport handshake failures (e.g. Safari).
		// DefaultConnectionTracer returns nil when QLOGDIR is unset.
		Tracer: qlog.DefaultConnectionTracer,
	}

	if dir := os.Getenv("QLOGDIR"); dir != "" {
		common.LogInfo("QLOGDIR set — writing QUIC qlog traces to %s", dir)
	}

	common.LogDebug("QUIC config initialized: MaxIdleTimeout=%v, KeepAlive=%v",
		s.quicConfig.MaxIdleTimeout, s.quicConfig.KeepAlivePeriod)
}

// GetQUICConfig returns the QUIC configuration (useful for testing)
func (s *MoqServer) GetQUICConfig() *quic.Config {
	return s.quicConfig
}

// GetTLSConfig returns the TLS configuration (useful for testing)
func (s *MoqServer) GetTLSConfig() *tls.Config {
	return s.tlsConfig
}
