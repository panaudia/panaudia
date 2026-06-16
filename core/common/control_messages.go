package common

type ControlMessage struct {
	MessageType string `json:"type"`
	NodeId      string `json:"node"`
	Message     J      `json:"message"`
}
