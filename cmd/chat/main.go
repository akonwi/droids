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
	"path/filepath"
	"strings"
	"sync"

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

	d, err := droids.New(droids.Options{
		Provider:     provider,
		Model:        model,
		SystemPrompt: "You are a helpful, concise CLI assistant with read-only filesystem access. Use the tools to inspect files and directories.",
		Tools:        fsTools(),
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

// workdir tracks the tools' current directory. read_file / list_files read it
// concurrently (parallel batch); change_dir mutates it and is ModeSequential,
// so the loop serializes any batch containing it. The RWMutex guards against a
// stray concurrent read regardless.
type workdir struct {
	mu  sync.RWMutex
	cwd string
}

func (w *workdir) get() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cwd
}

// resolve joins a possibly-relative path against the current directory.
func (w *workdir) resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(w.get(), p)
}

type pathArg struct {
	Path string `json:"path" jsonschema:"description=A file or directory path, absolute or relative to the current directory"`
}

// fsTools returns the read-only filesystem toolset.
func fsTools() []droids.AnyTool {
	start, err := os.Getwd()
	if err != nil {
		start = "."
	}
	wd := &workdir{cwd: start}

	readFile := droids.NewTool(droids.Tool[pathArg]{
		Name:        "read_file",
		Description: "Read and return the contents of a text file.",
		Execute: func(_ context.Context, args pathArg) (droids.ToolResult, error) {
			if args.Path == "" {
				return droids.ToolResult{}, fmt.Errorf("path is required")
			}
			data, err := os.ReadFile(wd.resolve(args.Path))
			if err != nil {
				return droids.ToolResult{}, err
			}
			return droids.ToolText(string(data)), nil
		},
	})

	listFiles := droids.NewTool(droids.Tool[pathArg]{
		Name:        "list_files",
		Description: "List the entries in a directory (like ls). Empty path lists the current directory.",
		Execute: func(_ context.Context, args pathArg) (droids.ToolResult, error) {
			dir := wd.get()
			if args.Path != "" {
				dir = wd.resolve(args.Path)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return droids.ToolResult{}, err
			}
			var b strings.Builder
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				fmt.Fprintln(&b, name)
			}
			return droids.ToolText(b.String()), nil
		},
	})

	changeDir := droids.NewTool(droids.Tool[pathArg]{
		Name:        "change_dir",
		Description: "Change the current working directory for subsequent tool calls (like cd).",
		// Mutates shared state; force the batch to serialize.
		Mode: droids.ModeSequential,
		Execute: func(_ context.Context, args pathArg) (droids.ToolResult, error) {
			if args.Path == "" {
				return droids.ToolResult{}, fmt.Errorf("path is required")
			}
			target := wd.resolve(args.Path)
			info, err := os.Stat(target)
			if err != nil {
				return droids.ToolResult{}, err
			}
			if !info.IsDir() {
				return droids.ToolResult{}, fmt.Errorf("%s is not a directory", target)
			}
			wd.mu.Lock()
			wd.cwd = target
			wd.mu.Unlock()
			return droids.ToolText("cwd is now " + target), nil
		},
	})

	return []droids.AnyTool{readFile, listFiles, changeDir}
}
