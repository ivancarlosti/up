package beszel

import "time"

// Container is one running (or recently stopped) Docker/Podman container.
//
// The deprecated MB fields (NetworkSent/NetworkRecv) are kept because agents
// older than 0.18.3 report only those; Bandwidth carries bytes since, and the
// ingest prefers it when it is not zero.
type Container struct {
	Name        string  `cbor:"0,keyasint"`
	Cpu         float64 `cbor:"1,keyasint"`
	Mem         float64 `cbor:"2,keyasint"`
	NetworkSent float64 `cbor:"3,keyasint,omitzero"`
	NetworkRecv float64 `cbor:"4,keyasint,omitzero"`
	// Health is one of the Health* constants below.
	Health          uint8     `cbor:"5,keyasint"`
	Status          string    `cbor:"6,keyasint"`
	ID              string    `cbor:"7,keyasint"`
	Image           string    `cbor:"8,keyasint"`
	Bandwidth       [2]uint64 `cbor:"9,keyasint,omitzero"`
	Ports           string    `cbor:"10,keyasint"`
	UpdateAvailable bool      `cbor:"11,keyasint,omitzero"`
}

// Health values of Container.Health.
const (
	HealthNone uint8 = iota
	HealthStarting
	HealthHealthy
	HealthUnhealthy
)

// HealthName renders Container.Health as the string the UI shows.
func HealthName(health uint8) string {
	switch health {
	case HealthStarting:
		return "starting"
	case HealthHealthy:
		return "healthy"
	case HealthUnhealthy:
		return "unhealthy"
	default:
		return ""
	}
}

// Smart is one disk of an ActionGetSmartData answer, already reduced by the
// agent to the attributes worth showing.
//
// The json tags are part of the contract, not a detail: the report is stored as
// it is decoded (models.SystemSmart.Disks) and served verbatim, so the keys the
// browser reads are the ones written here.
type Smart struct {
	ModelName       string      `cbor:"1,keyasint,omitempty" json:"model_name,omitempty"`
	SerialNumber    string      `cbor:"2,keyasint,omitempty" json:"serial_number,omitempty"`
	FirmwareVersion string      `cbor:"3,keyasint,omitempty" json:"firmware_version,omitempty"`
	Capacity        uint64      `cbor:"4,keyasint,omitempty" json:"capacity,omitempty"`
	Status          string      `cbor:"5,keyasint,omitempty" json:"status,omitempty"`
	DiskName        string      `cbor:"6,keyasint,omitempty" json:"disk_name,omitempty"`
	DiskType        string      `cbor:"7,keyasint,omitempty" json:"disk_type,omitempty"`
	Temperature     uint8       `cbor:"8,keyasint,omitempty" json:"temperature,omitempty"`
	Attributes      []SmartAttr `cbor:"9,keyasint,omitempty" json:"attributes,omitempty"`
}

// SmartAttr is one SMART attribute of a disk.
type SmartAttr struct {
	ID         uint16 `cbor:"0,keyasint,omitempty" json:"id,omitempty"`
	Name       string `cbor:"1,keyasint" json:"name"`
	Value      uint16 `cbor:"2,keyasint,omitempty" json:"value,omitempty"`
	Worst      uint16 `cbor:"3,keyasint,omitempty" json:"worst,omitempty"`
	Threshold  uint16 `cbor:"4,keyasint,omitempty" json:"threshold,omitempty"`
	RawValue   uint64 `cbor:"5,keyasint" json:"raw_value"`
	RawString  string `cbor:"6,keyasint,omitempty" json:"raw_string,omitempty"`
	WhenFailed string `cbor:"7,keyasint,omitempty" json:"when_failed,omitempty"`
}

// SmartReport is the normalised answer of ActionGetSmartData: the disks by
// device name, and whether every discovered device could be read.
type SmartReport struct {
	Disks    map[string]Smart `json:"disks"`
	Complete bool             `json:"complete"`
	At       time.Time        `json:"at"`
}
