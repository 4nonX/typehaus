// Package protocol defines the wire format between the typehaus server
// and the desktop sync agent. Keep it backwards compatible.
package protocol

// Version of the agent protocol. Bump on breaking changes.
const Version = 1

// EnrollRequest exchanges a one-time enrollment code for a device token.
type EnrollRequest struct {
	Code         string `json:"code"`
	DeviceName   string `json:"device_name"`
	Platform     string `json:"platform"` // windows, darwin, linux
	Hostname     string `json:"hostname"`
	AgentVersion string `json:"agent_version"`
}

type EnrollResponse struct {
	DeviceID int64  `json:"device_id"`
	Token    string `json:"token"`
	User     string `json:"user"`
}

// ManifestFont is one font file the device should have active.
type ManifestFont struct {
	ID       int64  `json:"id"`
	SHA256   string `json:"sha256"`
	Filename string `json:"filename"`
	Family   string `json:"family"`
	Style    string `json:"style"`
	FullName string `json:"full_name"`
	Format   string `json:"format"`
	Size     int64  `json:"size"`
}

// Manifest is the complete desired state for a device.
type Manifest struct {
	ProtocolVersion int            `json:"protocol_version"`
	User            string         `json:"user"`
	PollSeconds     int            `json:"poll_seconds"`
	Fonts           []ManifestFont `json:"fonts"`
}

// Report tells the server what is actually installed on the device.
type Report struct {
	AgentVersion string        `json:"agent_version"`
	Installed    []string      `json:"installed"` // sha256 list
	Errors       []ReportError `json:"errors,omitempty"`
}

type ReportError struct {
	SHA256  string `json:"sha256"`
	Message string `json:"message"`
}
