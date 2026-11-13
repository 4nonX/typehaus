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
	// PostScript is the font's PostScript name (the name design apps and
	// documents use); the app checks it against fonts already installed.
	PostScript string `json:"postscript_name,omitempty"`
	Format     string `json:"format"`
	Size       int64  `json:"size"`
	// Kind is sfnt (TrueType/OpenType), type1, winfnt, pcf or bdf.
	Kind string `json:"kind,omitempty"`
	// Extras are companion files installed next to the font with the same
	// base name, e.g. the .pfm/.afm metrics of a PostScript Type 1 font.
	Extras []ManifestExtra `json:"extras,omitempty"`
	// LeaseDays > 0: the agent removes the font after this many days without
	// contact to the server and installs it again once the server is back.
	LeaseDays int `json:"lease_days,omitempty"`
}

// ManifestExtra is a companion file of a font.
type ManifestExtra struct {
	SHA256 string `json:"sha256"`
	Ext    string `json:"ext"`
	Size   int64  `json:"size"`
}

// Manifest is the complete desired state for a device.
type Manifest struct {
	ProtocolVersion int            `json:"protocol_version"`
	User            string         `json:"user"`
	PollSeconds     int            `json:"poll_seconds"`
	ServerVersion   string         `json:"server_version,omitempty"`
	Fonts           []ManifestFont `json:"fonts"`
	// Inventory asks the agent to report the fonts installed outside
	// typehaus (at most once a day) to POST /api/agent/inventory.
	Inventory bool `json:"inventory,omitempty"`
	// ExistingFonts says what to do with a font whose PostScript name is
	// already installed on the computer outside typehaus: "" or "skip" –
	// don't install a second copy (report it); "install" – install anyway.
	ExistingFonts string `json:"existing_fonts,omitempty"`
}

// Existing is a library font the app did not install because a font with the
// same PostScript name is already on the computer outside typehaus.
type Existing struct {
	SHA256   string `json:"sha256"`    // the library font
	FullName string `json:"full_name"` // the library font
	Path     string `json:"path"`      // the copy already installed
	// Same: the installed copy is the identical file – nothing is missing.
	// Otherwise it is another version or another font with the same name.
	Same bool `json:"same"`
}

// InventoryFont is one font file found outside typehaus on a device.
type InventoryFont struct {
	Path       string `json:"path"`
	Scope      string `json:"scope"` // user | computer
	Family     string `json:"family"`
	Style      string `json:"style"`
	FullName   string `json:"full_name"`
	PostScript string `json:"postscript_name"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

// InventoryReport lists every font file found outside typehaus.
type InventoryReport struct {
	Fonts []InventoryFont `json:"fonts"`
}

// Report tells the server what is actually installed on the device.
type Report struct {
	AgentVersion string        `json:"agent_version"`
	Installed    []string      `json:"installed"` // sha256 list
	Errors       []ReportError `json:"errors,omitempty"`
	// Existing lists the fonts not installed because the computer already
	// has a font with the same name (see Manifest.ExistingFonts).
	Existing []Existing `json:"existing,omitempty"`
}

type ReportError struct {
	SHA256  string `json:"sha256"`
	Message string `json:"message"`
}

// ---- plugins (via the typehaus app) ------------------------------------------

// FontRef names a font the way a document does.
type FontRef struct {
	PostScript string `json:"postscript_name,omitempty"`
	Family     string `json:"family,omitempty"`
	Style      string `json:"style,omitempty"`
}

// FontStatus says what typehaus can do about a font a document uses.
type FontStatus struct {
	FontRef
	// active: the user has it (switched on or installed by a collection);
	// available: in the user's library, can be switched on; refused: in the
	// library, but a license doesn't allow it now (Reason); unsupported: its
	// format can't be installed on this computer's system (Reason);
	// not_available: not in the user's library – request it.
	Status   string `json:"status"`
	FontID   int64  `json:"font_id,omitempty"`
	SHA256   string `json:"sha256,omitempty"` // the file the app installs
	Family   string `json:"library_family,omitempty"`
	Style    string `json:"library_style,omitempty"`
	FullName string `json:"full_name,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type ResolveRequest struct {
	Fonts []FontRef `json:"fonts"`
}

type ResolveResponse struct {
	Fonts []FontStatus `json:"fonts"`
}
