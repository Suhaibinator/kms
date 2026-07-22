package invalid

import (
	"github.com/Suhaibinator/kms/internal/configgen/testdata/commonfragment"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type BothSources struct {
	Value string `json:"value" kms:"group=one,secret=two,reload=hot" kms_views:"api"`
}

func (*BothSources) Validate() error { return nil }

type UnknownClause struct {
	Value string `json:"value" kms:"group=one,reload=hot,wat=yes" kms_views:"api"`
}

func (*UnknownClause) Validate() error { return nil }

type DuplicateClause struct {
	Value string `json:"value" kms:"group=one,group=two,reload=hot" kms_views:"api"`
}

func (*DuplicateClause) Validate() error { return nil }

type MissingViews struct {
	Value string `json:"value" kms:"group=one,reload=hot"`
}

func (*MissingViews) Validate() error { return nil }

type DuplicateJSON struct {
	One string `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
	Two string `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*DuplicateJSON) Validate() error { return nil }

type Unsupported struct {
	Callback func() `json:"callback" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*Unsupported) Validate() error { return nil }

type RecursiveNode struct {
	Children []RecursiveNode `json:"children"`
}

type Recursive struct {
	Tree RecursiveNode `json:"tree" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*Recursive) Validate() error { return nil }

type Legacy struct {
	Value kmsclient.ParameterValue `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*Legacy) Validate() error { return nil }

type BadSecret struct {
	Value kmsclient.Secret `json:"value" kms:"secret=secret,reload=hot" kms_views:"api"`
}

func (*BadSecret) Validate() error { return nil }

type BadValidate struct {
	Value string `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*BadValidate) Validate() bool { return true }

type ViewCollision struct {
	One string `json:"one" kms:"group=one,reload=hot" kms_views:"a-b"`
	Two string `json:"two" kms:"group=one,reload=hot" kms_views:"a_b"`
}

func (*ViewCollision) Validate() error { return nil }

type UnexportedRoot struct {
	hidden []string
	Value  string `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*UnexportedRoot) Validate() error { return nil }

type ExcludedOpaque struct {
	Value    string `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
	Callback func() `kms:"-"`
}

func (*ExcludedOpaque) Validate() error { return nil }

type BadJSONName struct {
	Value string `json:"line.break" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*BadJSONName) Validate() error { return nil }

type BadNestedJSONNameValue struct {
	Value string `json:"line\nbreak"`
}

type BadNestedJSONName struct {
	Value BadNestedJSONNameValue `json:"value" kms:"group=one,reload=hot" kms_views:"api"`
}

func (*BadNestedJSONName) Validate() error { return nil }

type InlineScalar struct {
	Value string `kms:"inline"`
}

func (*InlineScalar) Validate() error { return nil }

type InlineViews struct {
	Common commonfragment.Config `kms:"inline" kms_views:"api"`
}

func (*InlineViews) Validate() error { return nil }

type PromotedValidate struct {
	commonfragment.Config `kms:"inline"`
}

type EmptyFragment struct {
	Local string `kms:"-"`
}

type EmptyInline struct {
	Empty EmptyFragment `kms:"inline"`
}

func (*EmptyInline) Validate() error { return nil }

type RecursiveInlineFragment struct {
	Value string                   `json:"value" kms:"group=runtime,reload=hot" kms_views:"api"`
	Next  *RecursiveInlineFragment `kms:"inline"`
}

type RecursiveInline struct {
	Fragment *RecursiveInlineFragment `kms:"inline"`
}

func (*RecursiveInline) Validate() error { return nil }

type FirstViewFragment struct {
	Value string `json:"first" kms:"group=one,reload=hot" kms_views:"api"`
}

type SecondViewFragment struct {
	Value string `json:"second" kms:"group=one,reload=hot" kms_views:"api"`
}

type DuplicateViewGetter struct {
	First  FirstViewFragment  `kms:"inline"`
	Second SecondViewFragment `kms:"inline"`
}

func (*DuplicateViewGetter) Validate() error { return nil }
