package main

// Distribution names the shape of a per-preset random selection.
type Distribution string

const (
	DistUniform Distribution = "uniform"
	DistMixed   Distribution = "mixed"
	DistZipf    Distribution = "zipf"
)

// Range holds an inclusive min/max for integer quantities like content size.
type Range struct {
	Min int
	Max int
}

// Preset is a named, fully deterministic workload specification.
type Preset struct {
	Name         string
	Users        int
	Rooms        int
	RoomSizeDist Distribution
	SenderDist   Distribution
	ContentBytes Range
	MentionRate  float64
	ThreadRate   float64
}

var builtinPresets = map[string]Preset{
	"small": {
		Name: "small", Users: 10, Rooms: 5,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"medium": {
		Name: "medium", Users: 1000, Rooms: 100,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"large": {
		Name: "large", Users: 10000, Rooms: 1000,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"realistic": {
		Name: "realistic", Users: 1000, Rooms: 100,
		RoomSizeDist: DistMixed, SenderDist: DistZipf,
		ContentBytes: Range{Min: 50, Max: 2000},
		MentionRate:  0.10,
		ThreadRate:   0.05,
	},
}

// BuiltinPreset looks up a preset by name.
func BuiltinPreset(name string) (Preset, bool) {
	p, ok := builtinPresets[name]
	return p, ok
}
