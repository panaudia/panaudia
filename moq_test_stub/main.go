package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/moq"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/panaudia/panaudia/core/space"
	"github.com/pion/webrtc/v3/pkg/media"
)

// ── Status file types ──────────────────────────────────────────────────

type NodeStatus struct {
	NodeId         string          `json:"nodeId"`
	Name           string          `json:"name"`
	Connected      bool            `json:"connected"`
	ConnectedAt    string          `json:"connectedAt"`
	DisconnectedAt string          `json:"disconnectedAt,omitempty"`
	AudioFrames    int             `json:"audioFrames"`
	Position       common.Position `json:"position"`
	Rotation       common.Rotation `json:"rotation"`
	Controls       []ControlEvent  `json:"controls"`
}

type ControlEvent struct {
	Type    string `json:"type"`
	NodeId  string `json:"nodeId"`
	Message any    `json:"message"`
}

type ServerStatus struct {
	Ready       bool                   `json:"ready"`
	Port        int                    `json:"port"`
	Connections map[string]*NodeStatus `json:"connections"`
	Errors      []string               `json:"errors"`
}

// ── Stub Backend ────────────────────────────────────────────────────────

type StubBackend struct {
	mu         sync.Mutex
	status     *ServerStatus
	statusPath string
	handlers   map[uuid.UUID]*StubConnectionHandler
}

func NewStubBackend(statusPath string, port int) *StubBackend {
	return &StubBackend{
		statusPath: statusPath,
		status: &ServerStatus{
			Ready:       false,
			Port:        port,
			Connections: make(map[string]*NodeStatus),
			Errors:      []string{},
		},
		handlers: make(map[uuid.UUID]*StubConnectionHandler),
	}
}

func (b *StubBackend) NewConnectionHandler(nodeConfig common.NodeConfig, outputTrack panaudia_server.TrackWriter) panaudia_server.ConnectionHandler {
	h := &StubConnectionHandler{
		nodeConfig:  nodeConfig,
		outputTrack: outputTrack,
		backend:     b,
		active:      false,
		deadCh:      make(chan uint64, 1),
	}
	b.mu.Lock()
	b.handlers[nodeConfig.Uuid] = h
	b.mu.Unlock()
	return h
}

func (b *StubBackend) FreeSource(id uuid.UUID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h, ok := b.handlers[id]; ok {
		// Mark disconnected in status
		if ns, ok := b.status.Connections[id.String()]; ok {
			ns.Connected = false
			ns.DisconnectedAt = time.Now().UTC().Format(time.RFC3339)
		}
		_ = h
		delete(b.handlers, id)
		b.writeStatus()
	}
}

func (b *StubBackend) recordConnect(nodeConfig common.NodeConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status.Connections[nodeConfig.Uuid.String()] = &NodeStatus{
		NodeId:      nodeConfig.Uuid.String(),
		Name:        nodeConfig.Name,
		Connected:   true,
		ConnectedAt: time.Now().UTC().Format(time.RFC3339),
		AudioFrames: 0,
		Position:    nodeConfig.Position,
		Rotation:    nodeConfig.Rotation,
		Controls:    []ControlEvent{},
	}
	b.writeStatus()
}

func (b *StubBackend) recordAudioFrame(id uuid.UUID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ns, ok := b.status.Connections[id.String()]; ok {
		ns.AudioFrames++
	}
	// Don't write status on every audio frame — too frequent
}

func (b *StubBackend) recordPosition(id uuid.UUID, pos common.Position) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ns, ok := b.status.Connections[id.String()]; ok {
		ns.Position = pos
	}
	b.writeStatus()
}

func (b *StubBackend) recordRotation(id uuid.UUID, rot common.Rotation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ns, ok := b.status.Connections[id.String()]; ok {
		ns.Rotation = rot
	}
	b.writeStatus()
}

func (b *StubBackend) recordControl(id uuid.UUID, msg common.ControlMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ns, ok := b.status.Connections[id.String()]; ok {
		ns.Controls = append(ns.Controls, ControlEvent{
			Type:    msg.MessageType,
			NodeId:  msg.NodeId,
			Message: msg.Message,
		})
	}
	b.writeStatus()
}

func (b *StubBackend) recordError(err string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status.Errors = append(b.status.Errors, err)
	b.writeStatus()
}

func (b *StubBackend) setReady() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status.Ready = true
	b.writeStatus()
}

// writeStatus writes the status to the JSON file. Caller must hold b.mu.
func (b *StubBackend) writeStatus() {
	data, err := json.MarshalIndent(b.status, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal status: %v\n", err)
		return
	}
	if err := os.WriteFile(b.statusPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write status file: %v\n", err)
	}
}

// ── Stub ConnectionHandler ─────────────────────────────────────────────

type StubConnectionHandler struct {
	nodeConfig  common.NodeConfig
	outputTrack panaudia_server.TrackWriter
	backend     *StubBackend
	active      bool
	deadCh      chan uint64
}

func (h *StubConnectionHandler) WriteOpus(src []byte) error {
	h.backend.recordAudioFrame(h.nodeConfig.Uuid)
	// Echo audio back to the client
	if h.outputTrack != nil {
		return h.outputTrack.WriteSample(media.Sample{
			Data:     src,
			Duration: 5 * time.Millisecond,
		})
	}
	return nil
}

func (h *StubConnectionHandler) SetPosition(position common.Position) {
	h.backend.recordPosition(h.nodeConfig.Uuid, position)
}

func (h *StubConnectionHandler) SetRotation(rotation common.Rotation) {
	h.backend.recordRotation(h.nodeConfig.Uuid, rotation)
}

func (h *StubConnectionHandler) Connect() *common.ServerError {
	h.active = true
	h.backend.recordConnect(h.nodeConfig)
	return nil
}

func (h *StubConnectionHandler) Stop() {
	h.active = false
}

func (h *StubConnectionHandler) IsActive() bool {
	return h.active
}

func (h *StubConnectionHandler) GetDeadSessionCh() chan uint64 {
	return h.deadCh
}

func (h *StubConnectionHandler) ControlMessage(msg common.ControlMessage) {
	h.backend.recordControl(h.nodeConfig.Uuid, msg)
}

func (h *StubConnectionHandler) SetReceiveSender(receiveSender space.IMessageSender) {
	// No-op for test stub — we don't relay state/attributes back
}

// ── Stub Authoriser ─────────────────────────────────────────────────────

type StubAuthoriser struct {
	ticketKeyPath string
}

func (a *StubAuthoriser) Authorise(queryValues map[string][]string) (common.NodeConfig, error) {
	return authoriseFromJWT(queryValues, a.ticketKeyPath)
}

func (a *StubAuthoriser) AuthoriseWithoutTicket(queryValues map[string][]string) (common.NodeConfig, error) {
	return common.NodeConfigFromQuery(queryValues)
}

// ── main ────────────────────────────────────────────────────────────────

func main() {
	port := flag.Int("port", 4433, "UDP port for QUIC/WebTransport")
	statusFile := flag.String("status-file", "moq_test_stub_status.json", "Path to write JSON status file")
	tlsCrt := flag.String("tls-crt", "keys/server.crt", "Path to TLS certificate")
	tlsKey := flag.String("tls-key", "keys/server.key", "Path to TLS private key")
	ticketKey := flag.String("ticket-key", "keys/panaudia_key.pub", "Path to JWT public key")
	flag.Parse()

	common.SetLogLevel(2) // Info level

	backend := NewStubBackend(*statusFile, *port)

	// Create authoriser (JWT validation using real key, no C deps)
	moqAuthoriser := &StubAuthoriser{ticketKeyPath: *ticketKey}

	moqConfig := moq.MoqServerConfig{
		Host:       "0.0.0.0",
		Port:       *port,
		TLSCrt:     *tlsCrt,
		TLSKey:     *tlsKey,
		MaxClients: 10,
	}

	moqServer, err := moq.NewMoqServer(moqConfig, backend, moqAuthoriser)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create MOQ server: %v\n", err)
		os.Exit(1)
	}

	if err := moqServer.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start MOQ server: %v\n", err)
		os.Exit(1)
	}

	backend.setReady()
	fmt.Printf("READY %d\n", *port)

	// Wait for SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("Shutting down...")
	if err := moqServer.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "Error stopping server: %v\n", err)
	}
}
