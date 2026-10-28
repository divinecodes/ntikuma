// Package tools provides bounded local tool execution. Arguments and returned
// text are untrusted model data; tool implementations enforce their own schema.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"ntikuma/internal/openrouter"
)

const MaxOutputChars = 20_000
const MaxArgumentBytes = 64 * 1024

type Tool interface {
	Def() openrouter.Function
	NeedsApproval() bool
	Run(context.Context, json.RawMessage) (string, error)
}

type registeredTool struct {
	tool Tool
	def  openrouter.Function
}

// Registry is safe for concurrent use. Definitions are frozen at registration;
// callers receive copies so they cannot mutate the registered JSON schemas.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]registeredTool
}

func (r *Registry) Register(tool Tool) error {
	if tool == nil {
		return errors.New("cannot register a nil tool")
	}
	def := tool.Def()
	if strings.TrimSpace(def.Name) == "" || len(def.Name) > 256 || def.Parameters == nil || def.Parameters["type"] != "object" {
		return errors.New("tool needs a name and an object parameter schema")
	}
	frozen, err := copyDefinition(def)
	if err != nil {
		return fmt.Errorf("invalid tool definition: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[string]registeredTool)
	}
	if _, exists := r.tools[def.Name]; exists {
		return fmt.Errorf("tool %q is already registered", def.Name)
	}
	r.tools[def.Name] = registeredTool{tool: tool, def: frozen}
	return nil
}

func copyDefinition(def openrouter.Function) (openrouter.Function, error) {
	raw, err := json.Marshal(def)
	if err != nil {
		return openrouter.Function{}, err
	}
	var copy openrouter.Function
	err = json.Unmarshal(raw, &copy)
	return copy, err
}

func (r *Registry) Defs() []openrouter.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	slices.Sort(names)
	defs := make([]openrouter.Tool, 0, len(names))
	for _, name := range names {
		def, _ := copyDefinition(r.tools[name].def) // Registration already verified JSON encoding.
		defs = append(defs, openrouter.Tool{Type: "function", Function: &def})
	}
	return defs
}

// Run returns errors as tool-result text. The turn runner must check its context
// separately to stop a canceled turn. Approval-bearing tools fail closed unless
// ExecuteWithApproval receives an explicit backend approval callback.
func (r *Registry) Run(ctx context.Context, call openrouter.ToolCall) string {
	output, _ := r.Execute(ctx, call)
	return output
}

// Execute returns the same bounded result text as Run, plus the execution error.
// Callers can report failures without mistaking a file starting with "error: "
// for a failed read. Tool errors still go back to the model as result text.
func (r *Registry) Execute(ctx context.Context, call openrouter.ToolCall) (string, error) {
	return r.ExecuteWithApproval(ctx, call, nil)
}

// ExecuteWithApproval validates the envelope before asking for permission. The
// callback must authorize this exact call; an absent callback fails closed.
func (r *Registry) ExecuteWithApproval(ctx context.Context, call openrouter.ToolCall, approve func(context.Context, openrouter.ToolCall) error) (string, error) {
	fail := func(err error) (string, error) { return capOutput("error: " + err.Error()), err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if call.Type != "function" {
		return fail(errors.New("unsupported tool-call type"))
	}
	r.mu.RLock()
	entry, ok := r.tools[call.Function.Name]
	r.mu.RUnlock()
	if !ok {
		return fail(fmt.Errorf("unknown tool %q", call.Function.Name))
	}
	args := json.RawMessage(call.Function.Arguments)
	if len(args) > MaxArgumentBytes {
		return fail(errors.New("tool arguments exceed 64 KiB"))
	}
	if !utf8.Valid(args) {
		return fail(errors.New("tool arguments must be valid UTF-8"))
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(args, &object); err != nil || object == nil {
		return fail(errors.New("tool arguments must be a JSON object"))
	}
	if entry.tool.NeedsApproval() {
		if approve == nil {
			return fail(errors.New("user approval is required before this tool can run"))
		}
		if err := approve(ctx, call); err != nil {
			return fail(err)
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
	}
	output, err := entry.tool.Run(ctx, args)
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return capOutput(output), nil
}

func capOutput(output string) string {
	output = strings.ToValidUTF8(output, "\uFFFD")
	if utf8.RuneCountInString(output) <= MaxOutputChars {
		return output
	}
	const suffix = "\n[truncated]"
	count := 0
	for offset := range output {
		if count == MaxOutputChars-len(suffix) {
			return output[:offset] + suffix
		}
		count++
	}
	return output
}
