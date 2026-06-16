package space

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
)

type Position = common.Position
type PolarPosition = common.PolarPosition
type Index = common.Index

//const FRAME_SIZE = common.FRAME_SIZE

type SpaceError struct {
	Code       int
	Message    string
	Data       map[string]string
	StatusCode int
}

func (e SpaceError) Error() string {
	jsonObject := common.J{"error": NODE_ERROR_NAMES[e.Code],
		"Message": NODE_ERROR_MESSAGES[e.Code],
		"Data":    e.Data}

	answerString, _ := json.Marshal(jsonObject)
	return string(answerString[:])
}

func NewSpaceError(code int, data map[string]string) SpaceError {
	return SpaceError{Code: code, Message: NODE_ERROR_MESSAGES[code], Data: data}
}

/////////////////

//type CellHandle struct {
//	Level int
//	Index Index
//}

//type testStruct struct {
//	Clip string `json:"clip"`
//}

type NodeSpec struct {
	Name     string                 `json:"name"`
	Uuid     string                 `json:"uuid"`
	Position Position               `json:"position"`
	Rotation Rotation               `json:"rotation"`
	Config   common.SpaceNodeConfig `json:"config"`

	// Session-instance identity for the cloud mixer leg
	// (plan/history/state-cleanup phase 6a, findings §6): Generation is the
	// gateway registry generation of the session this registration
	// belongs to; Gateway identifies the registering gateway process.
	// A re-registration with a NEWER generation from the SAME gateway
	// replaces the mixer-side session (fresh session id) instead of
	// adopting the departing one. Zero/empty = legacy caller →
	// reuse-as-before.
	Generation uint64 `json:"generation,omitempty"`
	Gateway    string `json:"gateway,omitempty"`
}

const ( // iota is reset to 0
	NODE_CHANGE_ADD         = iota
	NODE_CHANGE_MOVE        = iota
	NODE_CHANGE_DELETE      = iota
	NODE_CHANGE_ENABLE_OUT  = iota
	NODE_CHANGE_DISABLE_OUT = iota
	NODE_CHANGE_SOLO        = iota
	NODE_CHANGE_UNSOLO      = iota
	NODE_CHANGE_ADD_SUB     = iota
	NODE_CHANGE_REMOVE_SUB  = iota
)

const ( // iota is reset to 0
	COMMAND_CELL_UP_NODES     = iota
	COMMAND_CELL_UP_CELLS     = iota
	COMMAND_CELL_WEIGHTS      = iota
	COMMAND_CELL_ACROSS       = iota
	COMMAND_CELL_DOWN         = iota
	COMMAND_CELL_STOP         = iota
	COMMAND_CELL_DONE         = iota
	COMMAND_NODE_IN           = iota
	COMMAND_NODE_WEIGHTS      = iota
	COMMAND_NODE_ACROSS       = iota
	COMMAND_NODE_DOWN_AND_OUT = iota
	COMMAND_NODE_OUT          = iota
	COMMAND_NODE_STOP         = iota
	COMMAND_NODE_DONE         = iota
)

type NodeChange struct {
	Command     int
	Position    Position
	Rotation    Rotation
	Gain        float64
	Attenuation float64
	Name        string
	Uuid        uuid.UUID
	Style       map[string]string
	Config      common.SpaceNodeConfig
	OtherUuid   uuid.UUID
}

const ( // iota is reset to 0
	ERROR_NODE_COUNT_LIMIT    = iota // ERROR_NODE_LIMIT == 0
	ERROR_NODE_NAME_DUPLICATE = iota // ERROR_NODE_NAME_DUPLICATE == 1
	ERROR_NODE_NAME_MISSING   = iota // ERROR_NODE_NAME_MISSING == 2
	ERROR_CUBE_CLOSED         = iota // ERROR_CUBE_CLOSED == 3
	ERROR_CUBE_OUT_FULL       = iota // ERROR_CUBE_OUT_FULL == 4
	ERROR_CUBE_NODES_FULL     = iota // ERROR_CUBE_NODES_FULL == 5
	ERROR_CUBE_UUID_ERROR     = iota // ERROR_CUBE_NODES_FULL == 5
	ERROR_CUBE_INPUT_ERROR    = iota // ERROR_CUBE_INPUT_ERROR == 5
)

var NODE_ERROR_NAMES = map[int]string{
	ERROR_NODE_COUNT_LIMIT:    "ERROR_NODE_COUNT_LIMIT",
	ERROR_NODE_NAME_DUPLICATE: "ERROR_NODE_NAME_DUPLICATE",
	ERROR_NODE_NAME_MISSING:   "ERROR_NODE_NAME_MISSING",
	ERROR_CUBE_CLOSED:         "ERROR_CUBE_CLOSED",
	ERROR_CUBE_OUT_FULL:       "ERROR_CUBE_OUT_FULL",
	ERROR_CUBE_NODES_FULL:     "ERROR_CUBE_NODES_FULL",
	ERROR_CUBE_UUID_ERROR:     "ERROR_CUBE_UUID_ERROR",
	ERROR_CUBE_INPUT_ERROR:    "ERROR_CUBE_INPUT_ERROR",
}

var NODE_ERROR_MESSAGES = map[int]string{
	ERROR_NODE_COUNT_LIMIT:    "You have reached the limit for number of Nodes",
	ERROR_NODE_NAME_DUPLICATE: "This name already exists",
	ERROR_NODE_NAME_MISSING:   "This name is missing",
	ERROR_CUBE_CLOSED:         "BaseSpace is closed for changes to Nodes",
	ERROR_CUBE_OUT_FULL:       "BaseSpace Out full",
	ERROR_CUBE_NODES_FULL:     "BaseSpace Nodes full",
	ERROR_CUBE_UUID_ERROR:     "Couldn't parse UUID",
	ERROR_CUBE_INPUT_ERROR:    "Bad Input",
}
