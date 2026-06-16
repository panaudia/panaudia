package main

import (
	"crypto"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/golang-jwt/jwt/v5"
	"github.com/panaudia/panaudia/core/common"
)

// authoriseFromJWT validates a JWT token and returns a NodeConfig.
// This duplicates the essential logic from direct.DirectAuthoriser.CheckTicket
// to avoid importing the direct package (which pulls in C dependencies).
func authoriseFromJWT(queryValues map[string][]string, ticketKeyPath string) (common.NodeConfig, error) {
	ticket := queryValues["ticket"]
	if len(ticket) == 0 {
		return common.NodeConfig{}, errors.New("no ticket given")
	}

	publicKey, err := loadPublicKey(ticketKeyPath)
	if err != nil {
		return common.NodeConfig{}, fmt.Errorf("failed to load public key: %w", err)
	}

	type MyCustomClaims struct {
		Config            common.DirectTicketConfig `json:"panaudia"`
		PreferredUsername string                    `json:"preferred_username"`
		jwt.RegisteredClaims
	}

	token, err := jwt.ParseWithClaims(ticket[0], &MyCustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		return publicKey, nil
	})
	if err != nil {
		return common.NodeConfig{}, err
	}

	claims, ok := token.Claims.(*MyCustomClaims)
	if !ok {
		return common.NodeConfig{}, errors.New("couldn't read ticket claims")
	}

	claims.Config.Uuid = claims.RegisteredClaims.ID
	claims.Config.Name = claims.PreferredUsername

	nodeConfig, err := common.NodeConfigFromDirectTicket(claims.Config)
	if err != nil {
		return common.NodeConfig{}, err
	}

	nodeConfig = common.AddQueryAttrs(queryValues, nodeConfig)
	return nodeConfig, nil
}

var (
	cachedKey   crypto.PublicKey
	cachedKeyMu sync.Mutex
)

func loadPublicKey(path string) (crypto.PublicKey, error) {
	cachedKeyMu.Lock()
	defer cachedKeyMu.Unlock()

	if cachedKey != nil {
		return cachedKey, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read key file %s: %w", path, err)
	}

	key, err := jwt.ParseEdPublicKeyFromPEM(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Ed25519 public key: %w", err)
	}

	cachedKey = key
	return key, nil
}
