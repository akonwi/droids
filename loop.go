package droids

import (
	"context"
	"fmt"
	"sync"
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
			// Reject work dequeued after Close won the earlier race, so a
			// post-close prompt never mutates the transcript.
			select {
			case <-d.closed:
				if qp.result != nil {
					qp.result <- runResult{err: fmt.Errorf("droids: droid closed")}
				}
				d.finishEvents()
				return
			default:
			}
			msg := d.runPrompt(qp)
			if qp.result != nil {
				qp.result <- runResult{message: msg.message, err: msg.err}
			}
		}
	}
}

// finishEvents closes the event channel exactly once on shutdown. The channel
// is kept (not niled) so Events() keeps returning the same, now-closed channel.
func (d *Droid) finishEvents() {
	d.eventsMu.Lock()
	if !d.eventsClosed {
		d.eventsClosed = true
		if d.events != nil {
			close(d.events)
		}
	}
	d.eventsMu.Unlock()
}

// runPrompt executes one full agent run for the given prompt. The run context
// derives from the caller's ctx (Send/Run), so cancelling it aborts provider
// calls and tool execution; Abort and Close cancel it too.
func (d *Droid) runPrompt(qp queuedPrompt) runResult {
	base := qp.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	d.mu.Lock()
	d.cancelRun = cancel
	d.mu.Unlock()

	// Cancel the run when the droid is closed. The watcher exits when the run
	// finishes (stop) or the droid closes.
	stop := make(chan struct{})
	go func() {
		select {
		case <-d.closed:
			cancel()
		case <-stop:
		}
	}()

	defer func() {
		d.mu.Lock()
		d.cancelRun = nil
		d.mu.Unlock()
		cancel()
		close(stop)
	}()

	seed := qp.messages

	// If the prompt was cancelled while queued (caller ctx ended, or the droid
	// closed), drop it without emitting events or mutating the transcript.
	if err := ctx.Err(); err != nil {
		return runResult{
			message: AssistantMessage{
				Provider:     d.model.Provider,
				Model:        d.model.ID,
				StopReason:   StopReasonAborted,
				ErrorMessage: err.Error(),
				Timestamp:    time.Now().UnixMilli(),
			},
			err: err,
		}
	}

	d.emit(AgentStart{})

	// Append and announce the seed prompt(s).
	for _, m := range seed {
		d.appendMessage(ctx, m)
		d.emit(MessageStart{Message: m})
		d.emit(MessageEnd{Message: m})
	}

	var last AssistantMessage
	for step := 0; step < d.opts.MaxSteps; step++ {
		// Stop promptly if the run was cancelled (caller ctx, Abort, or Close)
		// rather than starting another turn.
		if err := ctx.Err(); err != nil {
			aborted := AssistantMessage{
				Provider:     d.model.Provider,
				Model:        d.model.ID,
				StopReason:   StopReasonAborted,
				ErrorMessage: err.Error(),
				Timestamp:    time.Now().UnixMilli(),
			}
			d.emit(ErrorEvent{Message: aborted, Aborted: true})
			d.emit(AgentEnd{Messages: d.snapshot()})
			return runResult{message: aborted, err: err}
		}

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
			// Preserve cancellation error identity (errors.Is) when the abort
			// was caused by the run context ending.
			if msg.StopReason == StopReasonAborted && ctx.Err() != nil {
				return runResult{message: msg, err: ctx.Err()}
			}
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

	stream := d.providers.Stream(ctx, d.model, req)

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

// toolOutcome is the finalized result of a single tool call.
type toolOutcome struct {
	result  ToolResult
	isError bool
}

// executeTools runs a batch of tool calls and returns their results in
// assistant source order. The batch runs sequentially when Options.ToolExecution
// is sequential or any tool in the batch is ModeSequential; otherwise tools
// execute concurrently. Regardless of execution order, ToolResultMessages are
// appended to the transcript in source order (deterministic for the model and
// resume); live ToolExecutionEnd events fire in completion order.
func (d *Droid) executeTools(ctx context.Context, calls []ToolCall) []ToolResultMessage {
	if d.batchIsSequential(calls) {
		return d.executeToolsSequential(ctx, calls)
	}
	return d.executeToolsParallel(ctx, calls)
}

func (d *Droid) batchIsSequential(calls []ToolCall) bool {
	if d.opts.ToolExecution == ModeSequential {
		return true
	}
	for _, call := range calls {
		if t, ok := d.tools[call.Name]; ok && t.mode() == ModeSequential {
			return true
		}
	}
	return false
}

func (d *Droid) executeToolsSequential(ctx context.Context, calls []ToolCall) []ToolResultMessage {
	var out []ToolResultMessage
	for _, call := range calls {
		d.emit(ToolExecutionStart{ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})
		result, isError := d.runToolCall(ctx, call)
		d.emit(ToolExecutionEnd{ToolCallID: call.ID, ToolName: call.Name, Result: result, IsError: isError})
		out = append(out, d.finalizeToolResult(ctx, call, result, isError))
		if ctx.Err() != nil {
			break
		}
	}
	return out
}

func (d *Droid) executeToolsParallel(ctx context.Context, calls []ToolCall) []ToolResultMessage {
	outcomes := make([]toolOutcome, len(calls))
	var execIdx []int

	// Preflight, sequentially in source order: emit starts and resolve before
	// hooks (block / short-circuit) deterministically. Survivors execute.
	for i, call := range calls {
		d.emit(ToolExecutionStart{ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})
		if oc, proceed := d.beforeTool(ctx, call); !proceed {
			outcomes[i] = oc
			d.emit(ToolExecutionEnd{ToolCallID: call.ID, ToolName: call.Name, Result: oc.result, IsError: oc.isError})
			continue
		}
		execIdx = append(execIdx, i)
	}

	// Execute survivors concurrently; emit ToolExecutionEnd in completion order.
	var wg sync.WaitGroup
	var sem chan struct{}
	if d.opts.MaxParallelTools > 0 {
		sem = make(chan struct{}, d.opts.MaxParallelTools)
	}
	for _, i := range execIdx {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}
			call := calls[i]
			result, isError := d.executeAndAfter(ctx, call)
			outcomes[i] = toolOutcome{result: result, isError: isError}
			d.emit(ToolExecutionEnd{ToolCallID: call.ID, ToolName: call.Name, Result: result, IsError: isError})
		}(i)
	}
	wg.Wait()

	// Finalize in source order: append + persist + emit message events.
	out := make([]ToolResultMessage, 0, len(calls))
	for i, call := range calls {
		oc := outcomes[i]
		out = append(out, d.finalizeToolResult(ctx, call, oc.result, oc.isError))
	}
	return out
}

// finalizeToolResult appends a tool result to the transcript, persists it, and
// emits its message lifecycle events.
func (d *Droid) finalizeToolResult(ctx context.Context, call ToolCall, result ToolResult, isError bool) ToolResultMessage {
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
	return trm
}

// runToolCall runs the full before/execute/after path for one call. Used by
// the sequential batch path.
func (d *Droid) runToolCall(ctx context.Context, call ToolCall) (ToolResult, bool) {
	if oc, proceed := d.beforeTool(ctx, call); !proceed {
		return oc.result, oc.isError
	}
	return d.executeAndAfter(ctx, call)
}

// beforeTool applies the before hook. proceed is false when the hook produced a
// terminal outcome (error, block, or short-circuit) and the tool must not run.
// A short-circuit result also skips the after hook. Hook errors degrade to an
// error result rather than crashing the loop.
func (d *Droid) beforeTool(ctx context.Context, call ToolCall) (oc toolOutcome, proceed bool) {
	if d.opts.BeforeToolCall == nil {
		return toolOutcome{}, true
	}
	br, err := d.opts.BeforeToolCall(ctx, BeforeToolContext{ToolCall: call, Args: call.Arguments})
	switch {
	case err != nil:
		return toolOutcome{result: ToolText(err.Error()), isError: true}, false
	case br.Block:
		reason := br.Reason
		if reason == "" {
			reason = "Tool execution was blocked"
		}
		return toolOutcome{result: ToolText(reason), isError: true}, false
	case br.Result != nil:
		// Short-circuit: skip execution and the after hook.
		return toolOutcome{result: *br.Result}, false
	}
	return toolOutcome{}, true
}

// executeAndAfter runs the tool and applies the after hook.
func (d *Droid) executeAndAfter(ctx context.Context, call ToolCall) (ToolResult, bool) {
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

// persistTimeout bounds a best-effort Storage.Append that is detached from run
// cancellation, so a stuck store cannot wedge the worker forever.
const persistTimeout = 30 * time.Second

func (d *Droid) appendMessage(ctx context.Context, m Message) {
	d.mu.Lock()
	d.transcript = append(d.transcript, m)
	d.mu.Unlock()
	// Persistence is independent of run cancellation: a message that completed
	// must be durably recorded even if the caller cancelled or the run was
	// aborted, so the transcript and storage stay consistent. Detach from the
	// run context but keep a bound so a hung store cannot block the worker.
	// Best-effort — a failing store must not crash the loop.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	_ = d.opts.Storage.Append(pctx, d.opts.Session, m)
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
