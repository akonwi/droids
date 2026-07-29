package droids

// model.go — model metadata and token accounting.

// Model describes a single model exposed by a provider. The registry resolves
// a user-facing model id string to one of these, tagged with its owning
// provider.
type Model struct {
	ID       string
	Name     string
	Provider string // owning provider id
	BaseURL  string

	Reasoning     bool
	Input         []string // "text", "image"
	ContextWindow int
	MaxTokens     int

	Cost Cost
}

// Cost is the per-million-token pricing for a model.
type Cost struct {
	Input      float64 // $/million input tokens
	Output     float64 // $/million output tokens
	CacheRead  float64
	CacheWrite float64
}

// Usage is the token accounting for one assistant turn.
type Usage struct {
	Input       int
	Output      int
	CacheRead   int
	CacheWrite  int
	Reasoning   int // subset of Output, when the provider reports it
	TotalTokens int
	Cost        UsageCost
}

// UsageCost is the computed dollar cost of a turn, derived from Usage + Model.Cost.
type UsageCost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

// calculateCost fills in u.Cost from the model pricing. Mirrors pi's
// calculateCost; kept simple (no Anthropic 1h split yet).
func calculateCost(m Model, u *Usage) {
	u.Cost.Input = m.Cost.Input / 1_000_000 * float64(u.Input)
	u.Cost.Output = m.Cost.Output / 1_000_000 * float64(u.Output)
	u.Cost.CacheRead = m.Cost.CacheRead / 1_000_000 * float64(u.CacheRead)
	u.Cost.CacheWrite = m.Cost.CacheWrite / 1_000_000 * float64(u.CacheWrite)
	u.Cost.Total = u.Cost.Input + u.Cost.Output + u.Cost.CacheRead + u.Cost.CacheWrite
}
