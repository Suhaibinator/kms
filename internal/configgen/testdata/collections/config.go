package collections

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type HTTPProviderOptions struct {
	Headers []Header `json:"headers"`
}
type ProviderConfig struct {
	HTTP HTTPProviderOptions `json:"http"`
}
type Config struct {
	Providers []ProviderConfig    `json:"inference_providers" kms:"group=genai,reload=hot" kms_views:"worker"`
	Items     []string            `json:"items" kms:"group=genai,reload=hot" kms_views:"worker"`
	Labels    map[string][]string `json:"labels" kms:"group=genai,reload=hot" kms_views:"worker"`
	Payload   []byte              `json:"payload" kms:"group=genai,reload=hot" kms_views:"worker"`
}

func (*Config) Validate() error { return nil }
