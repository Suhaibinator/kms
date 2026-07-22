package invalid

import "github.com/Suhaibinator/kms/sdk/go/kmsclient"

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
