package player

// mpv JSON IPC commands and properties. Grouped in one place so a protocol
// tweak never has to hunt through player code.
const (
	ipcCmdGetProperty = "get_property"
	ipcCmdSubAdd      = "sub-add"
	ipcSubSelect      = "select"

	ipcPropTimePos    = "time-pos"
	ipcPropDuration   = "duration"
	ipcPropPause      = "pause"
	ipcPropIdleActive = "idle-active"

	ipcSuccess = "success"
)
