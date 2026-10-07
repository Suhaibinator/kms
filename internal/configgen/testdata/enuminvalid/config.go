package enuminvalid

import (
	"math"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Blank is proto-shaped, and its Blank_ constant strips to an empty name.
type Blank int32

const (
	Blank_     Blank = 0
	Blank_TRUE Blank = 1
)

func (Blank) Descriptor() protoreflect.EnumDescriptor { return nil }
func (x Blank) Number() protoreflect.EnumNumber       { return protoreflect.EnumNumber(x) }

type EmptyName struct {
	Value Blank `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*EmptyName) Validate() error { return nil }

// Level has no member numbered zero.
type Level int8

const (
	LevelLow  Level = 1
	LevelHigh Level = 2
)

func (Level) String() string { return "level" }

type UndeclaredDefault struct {
	Value Level `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*UndeclaredDefault) Validate() error { return nil }

func UndeclaredDefaults() *UndeclaredDefault { return &UndeclaredDefault{} }

type Mode string

const (
	ModeFast Mode = "fast"
	ModeSafe Mode = "safe"
)

type UndeclaredStringDefault struct {
	Value []Mode `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*UndeclaredStringDefault) Validate() error { return nil }

func UndeclaredStringDefaults() *UndeclaredStringDefault {
	return &UndeclaredStringDefault{Value: []Mode{ModeFast, "turbo"}}
}

// Huge has a member above the largest enum number.
type Huge uint64

const (
	HugeZero Huge = 0
	HugeMax  Huge = math.MaxUint64
)

func (Huge) String() string { return "huge" }

type HugeNumber struct {
	Value Huge `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*HugeNumber) Validate() error { return nil }

// Wide is machine-sized, so its contract is the portable 32-bit width.
type Wide int

const (
	WideSmall Wide = 1
	WideLarge Wide = 1 << 40
)

func (Wide) String() string { return "wide" }

type Unportable struct {
	Value map[string]Wide `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*Unportable) Validate() error { return nil }
