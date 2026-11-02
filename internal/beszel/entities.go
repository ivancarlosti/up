package beszel

// Stats holds the counters of one sample.
//
// Several of the legacy "GB" fields (Mem, MemUsed, DiskTotal...) are still sent
// by every agent and are decoded here for agents older than the byte-sized
// ones: the ingest prefers MemBytes/DiskTotal and falls back to the GB values,
// which is what keeps a 0.13 agent readable through the same code path.
type Stats struct {
	CPU          float64            `cbor:"0,keyasint"`
	Mem          float64            `cbor:"2,keyasint"`
	MemUsed      float64            `cbor:"3,keyasint"`
	MemPct       float64            `cbor:"4,keyasint"`
	MemBuffCache float64            `cbor:"5,keyasint"`
	Swap         float64            `cbor:"7,keyasint,omitempty"`
	SwapUsed     float64            `cbor:"8,keyasint,omitempty"`
	DiskTotal    float64            `cbor:"9,keyasint"`
	DiskUsed     float64            `cbor:"10,keyasint"`
	DiskPct      float64            `cbor:"11,keyasint"`
	NetworkSent  float64            `cbor:"16,keyasint,omitzero"`
	NetworkRecv  float64            `cbor:"17,keyasint,omitzero"`
	Temperatures map[string]float64 `cbor:"20,keyasint,omitempty"`
	ExtraFs      map[string]*FsStat `cbor:"21,keyasint,omitempty"`
	// Bandwidth is [sent bytes, received bytes] per second.
	Bandwidth         [2]uint64            `cbor:"26,keyasint,omitzero"`
	LoadAvg           [3]float64           `cbor:"28,keyasint"`
	Battery           [2]uint8             `cbor:"29,keyasint,omitzero"`
	NetworkInterfaces map[string][4]uint64 `cbor:"31,keyasint,omitempty"`
	// DiskIO is [read bytes, written bytes] per second.
	DiskIO       [2]uint64 `cbor:"32,keyasint,omitzero"`
	CpuBreakdown []float64 `cbor:"33,keyasint,omitempty"`
	// DiskIoStats is [read time %, write time %, io utilization %, r_await,
	// w_await, weighted io %].
	DiskIoStats [6]float64        `cbor:"35,keyasint,omitzero"`
	Fans        map[string]uint16 `cbor:"36,keyasint,omitempty"`
	// DiskIOTotal is the cumulative device counter behind DiskIO.
	DiskIOTotal [2]uint64       `cbor:"38,keyasint,omitzero"`
	WiFi        map[string]int8 `cbor:"40,keyasint,omitempty"`
	MemBytes    [4]uint64       `cbor:"41,keyasint,omitzero"`
	SwapBytes   [2]uint64       `cbor:"42,keyasint,omitzero"`
}

// FsStat is one filesystem of Stats.ExtraFs.
type FsStat struct {
	DiskTotal      float64 `cbor:"0,keyasint"`
	DiskUsed       float64 `cbor:"1,keyasint"`
	DiskReadBytes  uint64  `cbor:"6,keyasint,omitempty"`
	DiskWriteBytes uint64  `cbor:"7,keyasint,omitempty"`
}

// Info is the dashboard summary of one sample plus the deprecated identity
// fields an old agent still sends.
type Info struct {
	// Hostname, KernelVersion and Cores are deprecated upstream (they moved to
	// SystemDetails) but are exactly what a pre-0.18 agent has.
	Hostname       string     `cbor:"0,keyasint,omitempty"`
	KernelVersion  string     `cbor:"1,keyasint,omitempty"`
	Cores          int        `cbor:"2,keyasint,omitzero"`
	Threads        int        `cbor:"3,keyasint,omitempty"`
	CpuModel       string     `cbor:"4,keyasint,omitempty"`
	Uptime         uint64     `cbor:"5,keyasint"`
	CPU            float64    `cbor:"6,keyasint"`
	MemPct         float64    `cbor:"7,keyasint"`
	DiskPct        float64    `cbor:"8,keyasint"`
	Bandwidth      float64    `cbor:"9,keyasint,omitzero"`
	AgentVersion   string     `cbor:"10,keyasint"`
	Podman         bool       `cbor:"11,keyasint,omitempty"`
	GpuPct         float64    `cbor:"12,keyasint,omitempty"`
	DashboardTemp  float64    `cbor:"13,keyasint,omitempty"`
	BandwidthBytes uint64     `cbor:"18,keyasint"`
	LoadAvg        [3]float64 `cbor:"19,keyasint"`
	// Services is [total, failed].
	Services     []uint16 `cbor:"22,keyasint,omitempty"`
	Battery      [2]uint8 `cbor:"23,keyasint,omitzero"`
	RootDiskName string   `cbor:"24,keyasint,omitempty"`
	// PackageUpdates is [total, security].
	PackageUpdates []uint16        `cbor:"25,keyasint,omitempty"`
	WiFi           map[string]WiFi `cbor:"26,keyasint,omitempty"`
}

// WiFi is a connected station interface.
type WiFi struct {
	SSID   string   `cbor:"0,keyasint,omitempty"`
	Signal *float64 `cbor:"1,keyasint,omitempty"`
}

// SystemDetails is the machine identity, sent only when the request asked for
// it (DataRequestOptions.IncludeDetails).
type SystemDetails struct {
	Hostname    string `cbor:"0,keyasint"`
	Kernel      string `cbor:"1,keyasint,omitempty"`
	Cores       int    `cbor:"2,keyasint"`
	Threads     int    `cbor:"3,keyasint"`
	CpuModel    string `cbor:"4,keyasint"`
	OS          uint8  `cbor:"5,keyasint"`
	OSName      string `cbor:"6,keyasint"`
	Arch        string `cbor:"7,keyasint"`
	Podman      bool   `cbor:"8,keyasint,omitempty"`
	MemoryTotal uint64 `cbor:"9,keyasint"`
	// SmartInterval and ZfsInterval are nanoseconds, as the agent sends a
	// time.Duration.
	SmartInterval int64 `cbor:"10,keyasint,omitempty"`
	ZfsInterval   int64 `cbor:"11,keyasint,omitempty"`
	Sockets       int   `cbor:"12,keyasint,omitempty"`
}
