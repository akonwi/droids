package droids

import "fmt"

// provider_cloudflare.go — Cloudflare AI Gateway support. The gateway is a
// proxy in front of the real provider APIs (caching, rate limiting, analytics,
// cost tracking), so it is transport, not a new model abstraction. This is a
// decorator: it points an existing OpenAI/Anthropic config at the gateway URL
// and adds the gateway auth header, returning a normal Provider.

// CloudflareGateway routes provider requests through a Cloudflare AI Gateway.
//
//	gw := droids.CloudflareGateway{AccountID: "...", GatewayID: "..."}
//	providers, _ := droids.NewProviders(
//		gw.OpenAI(droids.OpenAI{APIKey: openaiKey, Models: ...}),
//		gw.Anthropic(droids.Anthropic{APIKey: anthropicKey, Models: ...}),
//	)
type CloudflareGateway struct {
	// AccountID is the Cloudflare account id.
	AccountID string
	// GatewayID is the AI Gateway id.
	GatewayID string
	// Token authenticates to an authenticated gateway. When set it is sent as
	// the `cf-aig-authorization: Bearer <token>` header. Optional.
	Token string
	// Headers are extra gateway headers (e.g. cf-aig-cache-ttl, cf-aig-metadata,
	// cf-aig-skip-cache) merged into every request.
	Headers map[string]string
}

// baseURL builds the gateway endpoint for a provider slug (e.g. "openai").
func (g CloudflareGateway) baseURL(providerSlug string) string {
	return fmt.Sprintf(
		"https://gateway.ai.cloudflare.com/v1/%s/%s/%s",
		g.AccountID, g.GatewayID, providerSlug,
	)
}

// gatewayHeaders returns the cf-aig-* headers for the gateway.
func (g CloudflareGateway) gatewayHeaders() map[string]string {
	h := map[string]string{}
	for k, v := range g.Headers {
		h[k] = v
	}
	if g.Token != "" {
		h["cf-aig-authorization"] = "Bearer " + g.Token
	}
	return h
}

// OpenAI routes an OpenAI config through the gateway's `openai` endpoint,
// merging the gateway headers (config headers win on conflict).
func (g CloudflareGateway) OpenAI(cfg OpenAI) OpenAI {
	return g.OpenAICompatible("openai", cfg)
}

// OpenAICompatible routes an OpenAI config through an arbitrary gateway provider
// slug (e.g. "groq", "deepseek", "mistral", "cerebras", "workers-ai"). Any
// upstream that speaks the OpenAI chat-completions format works this way. Set
// cfg.ID to a distinct provider id when registering several alongside each
// other.
func (g CloudflareGateway) OpenAICompatible(slug string, cfg OpenAI) OpenAI {
	cfg.BaseURL = g.baseURL(slug)
	cfg.Headers = mergeHeaders(g.gatewayHeaders(), cfg.Headers)
	return cfg
}

// Compat routes an OpenAI config through the gateway's unified `compat`
// endpoint, where the upstream is chosen per request by the model id
// (e.g. "anthropic/claude-3-5-sonnet", "workers-ai/@cf/meta/llama-3.1-8b").
//
// Because those model ids contain "/", use this as a single gateway-backed
// provider rather than alongside providers whose id matches a model id's first
// segment; the registry resolves the full id, but a same-named sibling provider
// would shadow it.
func (g CloudflareGateway) Compat(cfg OpenAI) OpenAI {
	return g.OpenAICompatible("compat", cfg)
}

// Anthropic routes an Anthropic config through the gateway's `anthropic`
// endpoint, merging the gateway headers (config headers win on conflict).
func (g CloudflareGateway) Anthropic(cfg Anthropic) Anthropic {
	cfg.BaseURL = g.baseURL("anthropic")
	cfg.Headers = mergeHeaders(g.gatewayHeaders(), cfg.Headers)
	return cfg
}

// mergeHeaders returns base overlaid by override (override wins). A nil result
// is returned only when both are empty.
func mergeHeaders(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}
