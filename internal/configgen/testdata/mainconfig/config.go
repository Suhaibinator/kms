package main

type Config struct {
	Value string `json:"value" kms:"group=runtime,reload=hot" kms_views:"worker"`
}

func (*Config) Validate() error { return nil }

func main() {}
