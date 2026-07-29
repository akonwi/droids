package droids

import (
	"context"
	"fmt"
	"time"
)

// loop.go — the bounded multi-step tool loop (Layer 2). Consumes the queue
// filled by Send/Run, streams assistant turns from the provider, executes tool
// calls, and repeats until the model stops, MaxSteps is hit, or the run is
// aborted. Steering messages are drained between turns; each queued prompt is
// treated as its own run (follow-up).

func (d *Droid) worker() {
	for {
		select {
		case <-d.closed:
			d.finishEvents()
			return
		case qp := <-d.queue:
			msg := d.runPrompt(qp.messages)
			if qp.result != nil {
				qp.result <- runResult{message: msg.message, err: msg.err}
			}
		}
	}
}

func (d *Droid) finishEvents() {
	d.eventsMu.Lock()
	if d.events != nil {
		close(d.events)
		d.events = nil
	}
	d.eventsMu.Unlock()
}

// runPrompt executes one full agent run for the given seed messages.
func (d *Droid) runPrompt(seed []Message) runResult {
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.cancelRun = cancel
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.cancelRun = nil
		d.mu.Unlock()
		cancel()
	}()

	d.emit(AgentStart{})

	// Append and announce the seed prompt(s).
	for _, m := range seed {
		d.appendMessage(ctx, m)
		d.emit(MessageStart{Message: m})
		d.emit(MessageEnd{Message: m})
	}

	var last AssistantMessage
	for step := 0; step < d.opts.MaxSteps; step++ {
		d.emit(TurnStart{})

		// Drain steering messages before the turn.
		for _, m := range d.drainSteering() {
			d.appendMessage(ctx, m)
			d.emit(MessageStart{Message: m})
			d.emit(MessageEnd{Message: m})
		}

		msg := d.streamTurn(ctx)
		last = msg

		if msg.StopReason == StopReasonError || msg.StopReason == StopReasonAborted {
			d.emit(TurnEnd{Message: msg})
			d.emit(ErrorEvent{Message: msg, Aborted: msg.StopReason == StopReasonAborted})
			d.emit(AgentEnd{Messages: d.snapshot()})
			return runResult{message: msg, err: fmt.Errorf("%s", errText(msg))}
		}

		calls := msg.ToolCalls()
		if len(calls) == 0 {
			d.emit(TurnEnd{Message: msg})
			d.emit(AgentEnd{Messages: d.snapshot()})
			return runResult{message: msg}
		}

		results := d.executeTools(ctx, calls)
		d.emit(TurnEnd{Message: msg, ToolResults: results})
	}

	d.emit(AgentEnd{Messages: d.snapshot()})
	return runResult{message: last, err: fmt.Errorf("droids: reached MaxSteps (%d)", d.opts.MaxSteps)}
}

// streamTurn runs one provider stream and returns the final assistant message,
// emitting deltas and persisting the result.
func (d *Droid) streamTurn(ctx context.Context) AssistantMessage {
	req := Request{
		SystemPrompt: d.opts.SystemPrompt,
		Messages:     d.snapshot(),
		Tools:        d.toolSchemas(),
		Reasoning:    d.opts.Reasoning,
		MaxTokens:    d.model.MaxTokens,
	}

	stream := d.provider.Stream(ctx, d.model, req)

	started := false
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case StreamStart:
			started = true
			d.emit(MessageStart{Message: e.Partial})
		case StreamDone, StreamError:
			// handled after loop via Result()
		default:
			if started {
				d.emit(MessageDelta{Stream: ev})
			}
		}
	}

	final := stream.Result()
	calculateCost(d.model, &final.Usage)
	if final.Provider == "" {
		final.Provider = d.model.Provider
	}
	if final.Model == "" {
		final.Model = d.model.ID
	}
	d.appendMessage(ctx, final)
	if !started {
		d.emit(MessageStart{Message: final})
	}
	d.emit(MessageEnd{Message: final})
	return final
}

// executeTools runs each tool call sequentially and returns the results.
// Parallel execution is a future enhancement; ordering guarantees will be
// revisited then.
func (d *Droid) executeTools(ctx context.Context, calls []ToolCall) []ToolResultMessage {
	var out []ToolResultMessage
	for _, call := range calls {
		d.emit(ToolExecutionStart{ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})

		result, isError := d.runToolCall(ctx, call)

		d.emit(ToolExecutionEnd{ToolCallID: call.ID, ToolName: call.Name, Result: result, IsError: isError})

		trm := ToolResultMessage{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Content:    result.Content,
			Details:    result.Details,
			IsError:    isError,
			Timestamp:  time.Now().UnixMilli(),
		}
		d.appendMessage(ctx, trm)
		d.emit(MessageStart{Message: trm})
		d.emit(MessageEnd{Message: trm})
		out = append(out, trm)

		if ctx.Err() != nil {
			break
		}
	}
	return out
}

// runToolCall applies the before hook (block / short-circuit), executes the
// tool if not skipped, then applies the after hook. Hook errors degrade to an
// error result rather than crashing the loop.
func (d *Droid) runToolCall(ctx context.Context, call ToolCall) (ToolResult, bool) {
	if d.opts.BeforeToolCall != nil {
		br, err := d.opts.BeforeToolCall(ctx, BeforeToolContext{ToolCall: call, Args: call.Arguments})
		switch {
		case err != nil:
			return ToolText(err.Error()), true
		case br.Block:
			reason := br.Reason
			if reason == "" {
				reason = "Tool execution was blocked"
			}
			return ToolText(reason), true
		case br.Result != nil:
			// Short-circuit: skip execution and the after hook.
			return *br.Result, false
		}
	}

	result, isError := d.executeTool(ctx, call)

	if d.opts.AfterToolCall != nil {
		replacement, err := d.opts.AfterToolCall(ctx, AfterToolContext{ToolCall: call, Result: result, IsError: isError})
		if err != nil {
			return ToolText(err.Error()), true
		}
		if replacement != nil {
			return *replacement, isError
		}
	}
	return result, isError
}

// executeTool looks up and runs a single tool.
func (d *Droid) executeTool(ctx context.Context, call ToolCall) (ToolResult, bool) {
	tool, ok := d.tools[call.Name]
	if !ok {
		return ToolText(fmt.Sprintf("Tool %q not found", call.Name)), true
	}
	r, err := tool.execute(ctx, call.Arguments)
	if err != nil {
		return ToolText(err.Error()), true
	}
	return r, false
}

// --- transcript + steering helpers (mutex-guarded) ---

func (d *Droid) appendMessage(ctx context.Context, m Message) {
	d.mu.Lock()
	d.transcript = append(d.transcript, m)
	d.mu.Unlock()
	// Best-effort durability; a failing store must not crash the loop.
	_ = d.opts.Storage.Append(ctx, d.opts.Session, m)
}

func (d *Droid) snapshot() []Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Message, len(d.transcript))
	copy(out, d.transcript)
	return out
}

func (d *Droid) drainSteering() []Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.steering) == 0 {
		return nil
	}
	out := d.steering
	d.steering = nil
	return out
}

func (d *Droid) toolSchemas() []ToolSchema {
	out := make([]ToolSchema, 0, len(d.tools))
	for _, t := range d.tools {
		out = append(out, t.schema())
	}
	return out
}

func errText(m AssistantMessage) string {
	if m.ErrorMessage != "" {
		return m.ErrorMessage
	}
	return string(m.StopReason)
}
