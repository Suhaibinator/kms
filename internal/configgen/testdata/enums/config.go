package enums

import "google.golang.org/protobuf/reflect/protoreflect"

// Tier is shaped like a protoc-gen-go enum, including an allow_alias value.
type Tier int32

const (
	Tier_TIER_UNSPECIFIED  Tier = 0
	Tier_TIER_FREE         Tier = 1
	Tier_TIER_PRO          Tier = 3
	Tier_TIER_PROFESSIONAL Tier = 3
)

func (Tier) Descriptor() protoreflect.EnumDescriptor { return nil }
func (x Tier) Number() protoreflect.EnumNumber       { return protoreflect.EnumNumber(x) }
func (x Tier) String() string {
	switch x {
	case Tier_TIER_FREE:
		return "TIER_FREE"
	case Tier_TIER_PRO:
		return "TIER_PRO"
	default:
		return "TIER_UNSPECIFIED"
	}
}

// Plan_Interval is shaped like a nested protoc-gen-go enum: its values carry
// the parent message's prefix, not the enum's.
type Plan_Interval int32

const (
	Plan_INTERVAL_UNSPECIFIED Plan_Interval = 0
	Plan_INTERVAL_MONTHLY     Plan_Interval = 1
	Plan_INTERVAL_YEARLY      Plan_Interval = 2
)

func (Plan_Interval) Descriptor() protoreflect.EnumDescriptor { return nil }
func (x Plan_Interval) Number() protoreflect.EnumNumber       { return protoreflect.EnumNumber(x) }
func (x Plan_Interval) String() string                        { return "interval" }

// Sink is a hand-written iota enum.
type Sink uint8

const (
	SinkNone Sink = iota
	SinkStdout
	SinkFile
)

func (s Sink) String() string { return [...]string{"none", "stdout", "file"}[s] }

// Priority is declared out of numeric order and has an unprefixed constant,
// which is not a member.
type Priority int

const (
	PriorityHigh    Priority = 10
	PriorityLow     Priority = -1
	PriorityNormal  Priority = 0
	DefaultPriority          = PriorityNormal
)

func (p Priority) String() string { return "priority" }

// Adapter is a string enum; AdapterDefault repeats a value and is an alias.
type Adapter string

const (
	AdapterHTTP    Adapter = "http"
	AdapterGRPC    Adapter = "grpc"
	AdapterDefault Adapter = "http"
)

// Weight has typed constants but no String method, so it stays an integer.
type Weight int16

const WeightHeavy Weight = 9

// Label has no constants, so it stays a plain string.
type Label string

type Route struct {
	Adapter Adapter `json:"adapter"`
	Tier    *Tier   `json:"tier"`
}

type Config struct {
	Tier     Tier               `json:"tier" kms:"group=plan,reload=hot" kms_views:"api"`
	MinTier  *Tier              `json:"min_tier" kms:"group=plan,reload=hot" kms_views:"api"`
	Interval Plan_Interval      `json:"interval" kms:"group=plan,reload=restart" kms_views:"api"`
	Priority Priority           `json:"priority" kms:"group=plan,reload=hot" kms_views:"api"`
	Weight   Weight             `json:"weight" kms:"group=plan,reload=hot" kms_views:"api"`
	Label    Label              `json:"label" kms:"group=plan,reload=hot" kms_views:"api"`
	Primary  Adapter            `json:"primary" kms:"group=routing,reload=hot" kms_views:"api"`
	Sinks    []Sink             `json:"sinks" kms:"group=routing,reload=hot" kms_views:"api"`
	Adapters map[string]Adapter `json:"adapters" kms:"group=routing,reload=hot" kms_views:"api"`
	Routes   []Route            `json:"routes" kms:"group=routing,reload=hot" kms_views:"api"`
}

func (*Config) Validate() error { return nil }

func Defaults() *Config {
	return &Config{
		Tier:     Tier_TIER_FREE,
		MinTier:  new(Tier_TIER_PROFESSIONAL),
		Interval: Plan_INTERVAL_MONTHLY,
		Weight:   WeightHeavy,
		Label:    "main",
		Primary:  AdapterDefault,
		Sinks:    []Sink{SinkStdout, SinkFile},
		Adapters: map[string]Adapter{"primary": AdapterGRPC},
		Routes:   []Route{{Adapter: AdapterHTTP}},
	}
}
