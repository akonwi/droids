package droids

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// provider.go — the multiprovider registry. NewProvider composes one or more
// ProviderConfig values into a single Provider that routes each request to the
// config that owns the model. This mirrors pi-ai's Models registry.

// Provider is the model-abstraction layer. It lists models and streams a
// request against whichever registered provider owns the target model.
type Provider interface {
	// Models returns every model across all registered providers.
	Models() []Model
	// Model resolves a user-facing id to a concrete model. The id may be bare
	// ("gpt-5.6") or namespaced ("openai/gpt-5.6") to disambiguate.
	Model(id string) (Model, bool)
	// Stream runs a request against the provider that owns model.
	Stream(ctx context.Context, model Model, req Request) Stream
}

// ProviderConfig is an individual provider's configuration. Each config knows
// how to build itself into an internal entry (models + stream fn + auth).
type ProviderConfig interface {
	build() (providerEntry, error)
}

// providerEntry is the internal, resolved form of a ProviderConfig.
type providerEntry struct {
	id     string
	models map[string]Model
	stream streamFn
	call   callOptions
}

type registry struct {
	entries map[string]providerEntry // by provider id
	// index maps a bare model id to its owning provider id. Ambiguous ids
	// (served by multiple providers) are omitted; callers must namespace.
	index     map[string]string
	ambiguous map[string]bool
}

// NewProvider composes provider configs into a single routing Provider.
func NewProvider(configs ...ProviderConfig) (Provider, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("droids: NewProvider requires at least one ProviderConfig")
	}
	r := &registry{
		entries:   map[string]providerEntry{},
		index:     map[string]string{},
		ambiguous: map[string]bool{},
	}
	for _, cfg := range configs {
		entry, err := cfg.build()
		if err != nil {
			return nil, err
		}
		if _, dup := r.entries[entry.id]; dup {
			return nil, fmt.Errorf("droids: duplicate provider id %q", entry.id)
		}
		r.entries[entry.id] = entry
		for id := range entry.models {
			if _, seen := r.index[id]; seen {
				r.ambiguous[id] = true
				continue
			}
			r.index[id] = entry.id
		}
	}
	return r, nil
}

func (r *registry) Models() []Model {
	var out []Model
	for _, e := range r.entries {
		for _, m := range e.models {
			out = append(out, m)
		}
	}
	return out
}

func (r *registry) Model(id string) (Model, bool) {
	// Namespaced form: "provider/model".
	if provID, modelID, ok := strings.Cut(id, "/"); ok {
		if e, exists := r.entries[provID]; exists {
			if m, exists := e.models[modelID]; exists {
				return m, true
			}
		}
		// fall through: maybe the id legitimately contains a slash
	}
	if r.ambiguous[id] {
		return Model{}, false // caller must namespace
	}
	if provID, ok := r.index[id]; ok {
		m := r.entries[provID].models[id]
		return m, true
	}
	return Model{}, false
}

func (r *registry) Stream(ctx context.Context, model Model, req Request) Stream {
	e, ok := r.entries[model.Provider]
	if !ok {
		return erroredStream(model, fmt.Sprintf("unknown provider %q", model.Provider))
	}
	return e.stream(ctx, model, req, e.call)
}

// erroredStream returns a Stream that immediately fails. Used for
// configuration errors so failures flow through the normal stream protocol.
func erroredStream(model Model, msg string) Stream {
	final := AssistantMessage{
		Provider:     model.Provider,
		Model:        model.ID,
		StopReason:   StopReasonError,
		ErrorMessage: msg,
		Timestamp:    time.Now().UnixMilli(),
	}
	ch := make(chan StreamEvent, 1)
	ch <- StreamError{Message: final}
	close(ch)
	return &staticStream{events: ch, final: final}
}

// staticStream is a trivial Stream backed by a prebuilt channel + final message.
type staticStream struct {
	events <-chan StreamEvent
	final  AssistantMessage
}

func (s *staticStream) Events() <-chan StreamEvent { return s.events }
func (s *staticStream) Result() AssistantMessage   { return s.final }
