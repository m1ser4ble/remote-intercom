package protocol

type ConnectRequest struct {
	ChannelName   string `json:"channelName"`
	PIN           string `json:"pin"`
	DeviceName    string `json:"deviceName"`
	DeviceID      string `json:"deviceId,omitempty"`
	ClientVersion string `json:"clientVersion,omitempty"`
}

type ConnectResponse struct {
	Status        string `json:"status"`
	ChannelID     string `json:"channelId"`
	DeviceID      string `json:"deviceId"`
	JoinRequestID string `json:"joinRequestId,omitempty"`
	Token         string `json:"token"`
	WSURL         string `json:"wsUrl"`
}

type Event struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	ChannelID string         `json:"channelId,omitempty"`
	From      string         `json:"from,omitempty"`
	To        string         `json:"to,omitempty"`
	ReplyTo   string         `json:"replyTo,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}
