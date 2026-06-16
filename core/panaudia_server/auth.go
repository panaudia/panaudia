package panaudia_server

import (
	"github.com/panaudia/panaudia/core/common"
)

type Authoriser interface {
	Authorise(queryValues map[string][]string) (common.NodeConfig, error)
	AuthoriseWithoutTicket(queryValues map[string][]string) (common.NodeConfig, error)
	GetRocInConfig(queryValues map[string][]string) (common.RocInConnectConfig, error)
	GetRocOutConfig(queryValues map[string][]string) (common.RocOutputConfig, error)
}
