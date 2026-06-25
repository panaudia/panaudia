package direct

import (
	"crypto"
	"errors"
	"fmt"
	"os"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/commands"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/space"
)

type DirectAuthoriser struct {
	ticketed  bool
	publicKey crypto.PublicKey
	spaceId   string
	order     int
	// authorizer is consulted to flatten the holder's roles into a set
	// of read capabilities (commands.ResolveReadCaps) which are stamped
	// onto the NodeConfig at validation time. Independent of command
	// dispatch — that path queries the same authorizer separately via
	// the BouncerClient's commandAuthorizer reference.
	authorizer commands.Authorizer
	// kickGate refuses authorisation for an in-effect kick (see
	// core/space/kick_gate.go). May be nil — Authorise treats nil as
	// "no kick enforcement" (e.g. tests, ROC-only deployments without
	// a cache).
	kickGate *space.KickGate
}

func mustNot(err error) {
	if err != nil {
		panic(err)
	}
}

func NewDirectAuthoriser(ticketKeyPath string, order int, authorizer commands.Authorizer, kickGate *space.KickGate) *DirectAuthoriser {

	if ticketKeyPath == "" {
		return &DirectAuthoriser{ticketed: false, publicKey: nil, order: order, authorizer: authorizer, kickGate: kickGate}
	} else {
		publicKeyBytes, err1 := os.ReadFile(ticketKeyPath)
		if err1 != nil {
			msg := fmt.Sprintf("Failed to read file given by PANAUDIA_TICKET_KEY_PATH at %v: %v", ticketKeyPath, err1)
			panic(msg)
		}

		key, err2 := jwt.ParseEdPublicKeyFromPEM(publicKeyBytes)
		if err2 != nil {
			msg := fmt.Sprintf("Failed to parse the ticket key file. It sould be a ECDSA Ed25519 public key in PEM format: %v", err2)
			panic(msg)
		}
		return &DirectAuthoriser{ticketed: true, publicKey: key, order: order, authorizer: authorizer, kickGate: kickGate}
	}
}

// checkKick returns an error if the gate has an in-effect kick for
// this NodeConfig. Returns nil if no gate is wired, no kick is set,
// or the kick has already expired.
func (authoriser *DirectAuthoriser) checkKick(config common.NodeConfig) error {
	if authoriser.kickGate == nil {
		return nil
	}
	if kicked, deadline := authoriser.kickGate.IsKicked(config.Uuid, config.Roles); kicked {
		if deadline.IsZero() {
			return fmt.Errorf("kicked: refusing %s (forever)", config.Uuid)
		}
		return fmt.Errorf("kicked: refusing %s until %s", config.Uuid, deadline.UTC().Format("2006-01-02T15:04:05Z"))
	}
	return nil
}

// resolveReadCaps stamps the resolved read scopes onto the NodeConfig.
// Called from each NodeConfig-producing path so any consumer (writers,
// future filter sites) can rely on ReadCaps being populated.
func (authoriser *DirectAuthoriser) resolveReadCaps(config *common.NodeConfig) {
	if authoriser.authorizer == nil {
		return
	}
	config.ReadCaps = commands.ResolveReadCaps(authoriser.authorizer, config.Roles)
}

func (authoriser *DirectAuthoriser) Authorise(queryValues map[string][]string) (common.NodeConfig, error) {

	if !authoriser.ticketed {
		return common.NodeConfig{}, errors.New("No public key given to check ticket against")
	}

	ticket := queryValues["ticket"]

	var config common.NodeConfig
	var err error

	if len(ticket) > 0 {
		config, err = authoriser.CheckTicket(ticket[0])
	} else {
		config, err = common.NodeConfig{}, errors.New("No ticket given in query string")
	}

	if err == nil {
		config = common.AddQueryAttrs(queryValues, config)
		authoriser.resolveReadCaps(&config)
		if kerr := authoriser.checkKick(config); kerr != nil {
			return common.NodeConfig{}, kerr
		}
	} else {
		println(err.Error())
	}

	return config, err
}

func (authoriser *DirectAuthoriser) AuthoriseWithoutTicket(queryValues map[string][]string) (common.NodeConfig, error) {

	config, err := common.NodeConfigFromQuery(queryValues)
	if err == nil {
		config = common.AddQueryAttrs(queryValues, config)
		authoriser.resolveReadCaps(&config)
		if kerr := authoriser.checkKick(config); kerr != nil {
			return common.NodeConfig{}, kerr
		}
	}
	return config, err
}

func (authoriser *DirectAuthoriser) CheckTicket(ticket string) (common.NodeConfig, error) {

	type MyCustomClaims struct {
		Config            common.DirectTicketConfig `json:"panaudia"`
		PreferredUsername string                    `json:"preferred_username"`
		jwt.RegisteredClaims
	}

	token, err := jwt.ParseWithClaims(ticket, &MyCustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		return authoriser.publicKey, nil
	})

	if err != nil {
		//fmt.Printf("err: %v", err)
		return common.NodeConfig{}, err
	} else if claims, ok := token.Claims.(*MyCustomClaims); ok {
		claims.Config.Uuid = claims.RegisteredClaims.ID
		claims.Config.Name = claims.PreferredUsername
		//fmt.Printf("claims: %v", claims)
		//TODO update for new structure
		nodeConfig, err2 := common.NodeConfigFromDirectTicket(claims.Config)
		if err2 != nil {
			common.LogError("NodeConfigFromTicket error: %v", err2)
			return common.NodeConfig{}, err2
		}
		return nodeConfig, nil
	} else {
		return common.NodeConfig{}, errors.New("Couldn't read ticket")
	}
}

func (authoriser *DirectAuthoriser) GetRocInConfig(queryValues map[string][]string) (common.RocInConnectConfig, error) {

	return common.RocInConnectConfig{}, nil
}

func (authoriser *DirectAuthoriser) GetRocOutConfig(queryValues map[string][]string) (common.RocOutputConfig, error) {

	nodeConfig := common.NodeConfig{}
	var err error

	uuids := queryValues["uuid"]
	if len(uuids) == 0 {
		return common.RocOutputConfig{}, errors.New("Unauthorised")
	} else {
		id, err := uuid.Parse(uuids[0])
		if err != nil {
			return common.RocOutputConfig{}, err
		}
		nodeConfig.Uuid = id
	}

	names := queryValues["name"]
	if len(names) == 0 {
		return common.RocOutputConfig{}, errors.New("Unauthorised")
	} else {
		nodeConfig.Name = names[0]
	}

	ports := common.RocPorts{}

	hosts := queryValues["host"]
	if len(hosts) == 0 {
		return common.RocOutputConfig{}, errors.New("Unauthorised")
	} else {
		ports.Host = hosts[0]
	}

	normalisation := "SN3D"

	normalisations := queryValues["normalisation"]
	if len(normalisations) >= 0 {
		normalisation = normalisations[0]
	}

	ports.Source, err = common.Uint32FromQuery("source", queryValues)

	if err != nil {
		return common.RocOutputConfig{}, errors.New("Unauthorised")
	}

	ports.Repair, err = common.Uint32FromQuery("repair", queryValues)

	if err != nil {
		return common.RocOutputConfig{}, errors.New("Unauthorised")
	}

	ports.Control, err = common.Uint32FromQuery("control", queryValues)

	if err != nil {
		return common.RocOutputConfig{}, errors.New("Unauthorised")
	}

	channelCount, err2 := common.Uint32FromQuery("channels", queryValues)

	//common.LogDebug(fmt.Sprintf("channelCount: %v", channelCount))
	//common.LogDebug(fmt.Sprintf("order: %v", authoriser.order))
	//common.LogDebug(fmt.Sprintf("ChannelCountForOrder: %v", common.ChannelCountForOrder(authoriser.order)))

	if err2 != nil {
		return common.RocOutputConfig{}, errors.New("Unauthorised")
	}

	if channelCount != uint32(common.ChannelCountForOrder(authoriser.order)) {
		return common.RocOutputConfig{}, errors.New("Ambisonic order mismatch")
	}

	nodeConfig.SubSpaces, err = common.SubspacesFromQuery(queryValues)
	if err != nil {
		return common.RocOutputConfig{}, err
	}

	nodeConfig.Position = common.PositionFromFromQuery(queryValues)
	nodeConfig.ReturnData = false
	nodeConfig.Gain = 1.0
	nodeConfig.Attenuation = 2.0
	nodeConfig.Input = false
	nodeConfig.Priority = true
	nodeConfig.Tone = 0.0
	nodeConfig.NullOut = false
	nodeConfig.Raw = false
	return common.RocOutputConfig{Node: nodeConfig,
		Ports:         ports,
		Channels:      common.ChannelCountForOrder(authoriser.order),
		Normalisation: normalisation}, nil

}
