package player

// mpv JSON IPC commands, properties, and envelope fields. Grouped in one
// place so a protocol tweak never has to hunt through player code.
const (
	ipcCmdGetProperty = "get_property"
	ipcCmdSubAdd      = "sub-add"
	ipcSubSelect      = "select"

	ipcPropTimePos    = "time-pos"
	ipcPropDuration   = "duration"
	ipcPropPause      = "pause"
	ipcPropIdleActive = "idle-active"

	ipcFieldCommand   = "command"
	ipcFieldRequestID = "request_id"
	ipcFieldError     = "error"
	ipcFieldData      = "data"
	ipcSuccess        = "success"
)
