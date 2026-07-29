// Command chat is a minimal CLI REPL for exercising droids against a real
// OpenAI-compatible endpoint.
//
//	OPENAI_API_KEY=sk-... go run ./cmd/chat
//
// Optional environment:
//
//	DROIDS_BASE_URL  route through an AI Gateway / compatible provider
//	DROIDS_MODEL     model id (default: gpt-4o-mini)
//
// Type a message and press enter. Ctrl-C aborts the in-flight turn; Ctrl-D
// (EOF) or "exit" quits.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/akonwi/droids"
)

func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "set OPENAI_API_KEY")
		os.Exit(1)
	}
	model := os.Getenv("DROIDS_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}

	provider, err := droids.NewProvider(droids.OpenAI{
		APIKey:  apiKey,
		BaseURL: os.Getenv("DROIDS_BASE_URL"),
		Models:  []droids.Model{{ID: model, MaxTokens: 1024}},
	})
	if err != nil {
		fatal(err)
	}

	clock := droids.NewTool(droids.Tool[struct{}]{
		Name:        "get_time",
		Description: "Get the current server time (RFC3339).",
		Execute: func(_ context.Context, _ struct{}) (droids.ToolResult, error) {
			return droids.ToolText(time.Now().Format(time.RFC3339)), nil
		},
	})

	d, err := droids.New(droids.Options{
		Provider:     provider,
		Model:        model,
		SystemPrompt: "You are a helpful, concise CLI assistant.",
		Tools:        []droids.AnyTool{clock},
	})
	if err != nil {
		fatal(err)
	}
	defer d.Close()

	// Ctrl-C aborts the active turn (rather than killing the process).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		for range sig {
			d.Abort()
		}
	}()

	// Render events in the background. `turnDone` signals when the agent is
	// idle again so the REPL can print the next prompt.
	turnDone := make(chan struct{}, 1)
	go render(d.Events(), turnDone)

	fmt.Printf("droids chat — model %q. Ctrl-D to quit.\n", model)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Print("\nyou › ")
		if !in.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return
		}
		if err := d.Send(context.Background(), line); err != nil {
			fatal(err)
		}
		<-turnDone // wait for the turn to finish before prompting again
	}
}

// render consumes the droid's event stream and prints a chat-like transcript.
func render(events <-chan droids.Event, turnDone chan<- struct{}) {
	printingAssistant := false
	for ev := range events {
		switch e := ev.(type) {
		case droids.MessageDelta:
			if td, ok := e.Stream.(droids.StreamTextDelta); ok {
				if !printingAssistant {
					fmt.Print("\nbot › ")
					printingAssistant = true
				}
				fmt.Print(td.Delta)
			}

		case droids.ToolExecutionStart:
			fmt.Printf("\n  ⚙ %s(%s)", e.ToolName, strings.TrimSpace(string(e.Arguments)))

		case droids.ToolExecutionEnd:
			status := "ok"
			if e.IsError {
				status = "error"
			}
			fmt.Printf(" → %s", status)

		case droids.ErrorEvent:
			fmt.Printf("\n  ✗ %s\n", e.Message.ErrorMessage)

		case droids.TurnStart:
			printingAssistant = false

		case droids.AgentEnd:
			fmt.Println()
			select {
			case turnDone <- struct{}{}:
			default:
			}
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
