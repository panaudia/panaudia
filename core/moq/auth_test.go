package moq

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
)

// generateTestEd25519Keys generates a test Ed25519 key pair
func generateTestEd25519Keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate Ed25519 keys: %v", err)
	}
	return publicKey, privateKey
}

// generateTestJWT creates a valid test JWT token
func generateTestJWT(t *testing.T, privateKey ed25519.PrivateKey, config common.DirectTicketConfig) string {
	type MyCustomClaims struct {
		Config            common.DirectTicketConfig `json:"panaudia"`
		PreferredUsername string                    `json:"preferred_username"`
		jwt.RegisteredClaims
	}

	claims := MyCustomClaims{
		Config:            config,
		PreferredUsername: config.Name,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        config.Uuid,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("Failed to sign JWT: %v", err)
	}

	return tokenString
}

// testAuthoriser is a test implementation that uses DirectAuthoriser
type testAuthoriser struct {
	publicKey ed25519.PublicKey
}

func (a *testAuthoriser) Authorise(queryValues map[string][]string) (common.NodeConfig, error) {
	ticket := queryValues["ticket"]
	if len(ticket) == 0 {
		return common.NodeConfig{}, jwt.ErrTokenMalformed
	}

	type MyCustomClaims struct {
		Config            common.DirectTicketConfig `json:"panaudia"`
		PreferredUsername string                    `json:"preferred_username"`
		jwt.RegisteredClaims
	}

	token, err := jwt.ParseWithClaims(ticket[0], &MyCustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		return a.publicKey, nil
	})

	if err != nil {
		return common.NodeConfig{}, err
	}

	if claims, ok := token.Claims.(*MyCustomClaims); ok {
		claims.Config.Uuid = claims.RegisteredClaims.ID
		claims.Config.Name = claims.PreferredUsername
		return common.NodeConfigFromDirectTicket(claims.Config)
	}

	return common.NodeConfig{}, jwt.ErrTokenInvalidClaims
}

func (a *testAuthoriser) AuthoriseWithoutTicket(queryValues map[string][]string) (common.NodeConfig, error) {
	return common.NodeConfigFromQuery(queryValues)
}

// TestAuthenticateToken tests JWT token authentication
func TestAuthenticateToken(t *testing.T) {
	// Generate test keys
	publicKey, privateKey := generateTestEd25519Keys(t)

	// Create test config
	testUUID := uuid.New()
	testConfig := common.DirectTicketConfig{
		Uuid:        testUUID.String(),
		Name:        "TestUser",
		Gain:        1.0,
		Attenuation: 2.0,
		Priority:    false,
		Attrs:       common.J{"test": "value"},
	}

	// Generate a valid JWT
	validToken := generateTestJWT(t, privateKey, testConfig)

	// Create authoriser
	authoriser := &testAuthoriser{publicKey: publicKey}

	// Create a SessionSubscribeHandler
	session := &MoqSession{}
	handler := NewSessionSubscribeHandler(session, authoriser, false)

	// Test authentication
	nodeConfig, err := handler.authenticateToken(validToken)
	if err != nil {
		t.Fatalf("Failed to authenticate valid token: %v", err)
	}

	// Verify the node config
	if nodeConfig.Name != "TestUser" {
		t.Errorf("Expected name 'TestUser', got '%s'", nodeConfig.Name)
	}

	if nodeConfig.Uuid != testUUID {
		t.Errorf("Expected UUID %s, got %s", testUUID, nodeConfig.Uuid)
	}

	if nodeConfig.Gain != 1.0 {
		t.Errorf("Expected gain 1.0, got %f", nodeConfig.Gain)
	}

	if nodeConfig.Attenuation != 2.0 {
		t.Errorf("Expected attenuation 2.0, got %f", nodeConfig.Attenuation)
	}
}

// TestAuthenticateTokenInvalid tests authentication with invalid token
func TestAuthenticateTokenInvalid(t *testing.T) {
	// Generate test keys
	publicKey, _ := generateTestEd25519Keys(t)

	// Create authoriser
	authoriser := &testAuthoriser{publicKey: publicKey}

	// Create a SessionSubscribeHandler
	session := &MoqSession{}
	handler := NewSessionSubscribeHandler(session, authoriser, false)

	// Test with invalid token
	_, err := handler.authenticateToken("invalid.jwt.token")
	if err == nil {
		t.Error("Expected error for invalid token, got nil")
	}
}

// TestAuthenticateTokenExpired tests authentication with expired token
func TestAuthenticateTokenExpired(t *testing.T) {
	// Generate test keys
	publicKey, privateKey := generateTestEd25519Keys(t)

	// Create test config
	testUUID := uuid.New()
	testConfig := common.DirectTicketConfig{
		Uuid:        testUUID.String(),
		Name:        "TestUser",
		Gain:        1.0,
		Attenuation: 2.0,
		Priority:    false,
		Attrs:       common.J{},
	}

	// Create claims with expired timestamp
	type MyCustomClaims struct {
		Config            common.DirectTicketConfig `json:"panaudia"`
		PreferredUsername string                    `json:"preferred_username"`
		jwt.RegisteredClaims
	}

	claims := MyCustomClaims{
		Config:            testConfig,
		PreferredUsername: testConfig.Name,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        testConfig.Uuid,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)), // Expired 1 hour ago
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	expiredToken, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("Failed to sign JWT: %v", err)
	}

	// Create authoriser
	authoriser := &testAuthoriser{publicKey: publicKey}

	// Create a SessionSubscribeHandler
	session := &MoqSession{}
	handler := NewSessionSubscribeHandler(session, authoriser, false)

	// Test with expired token
	_, err = handler.authenticateToken(expiredToken)
	if err == nil {
		t.Error("Expected error for expired token, got nil")
	}
}

// TestValidateNodeConfig tests node config validation
func TestValidateNodeConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      common.NodeConfig
		expectError bool
	}{
		{
			name: "valid config",
			config: common.NodeConfig{
				Uuid: uuid.New(),
				Name: "Test",
				SpaceNodeConfig: common.SpaceNodeConfig{
					Gain:        1.0,
					Attenuation: 2.0,
				},
			},
			expectError: false,
		},
		{
			name: "zero UUID",
			config: common.NodeConfig{
				Uuid: uuid.UUID{},
				Name: "Test",
				SpaceNodeConfig: common.SpaceNodeConfig{
					Gain:        1.0,
					Attenuation: 2.0,
				},
			},
			expectError: true,
		},
		{
			name: "gain too high",
			config: common.NodeConfig{
				Uuid: uuid.New(),
				Name: "Test",
				SpaceNodeConfig: common.SpaceNodeConfig{
					Gain:        5.0,
					Attenuation: 2.0,
				},
			},
			expectError: true,
		},
		{
			name: "attenuation negative",
			config: common.NodeConfig{
				Uuid: uuid.New(),
				Name: "Test",
				SpaceNodeConfig: common.SpaceNodeConfig{
					Gain:        1.0,
					Attenuation: -1.0,
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNodeConfig(tt.config)
			if tt.expectError && err == nil {
				t.Error("Expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error, got: %v", err)
			}
		})
	}
}
