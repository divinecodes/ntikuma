package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"ntikuma/internal/openrouter"
)

type fixtureTool struct {
	definition openrouter.Function
	approval   bool
	output     string
	err        error
	calls      atomic.Int32
	run        func(context.Context) (string, error)
}

func (t *fixtureTool) Def() openrouter.Function { return t.definition }
func (t *fixtureTool) NeedsApproval() bool      { return t.approval }
func (t *fixtureTool) Run(ctx context.Context, _ json.RawMessage) (string, error) {
	t.calls.Add(1)
	if t.run != nil {
		return t.run(ctx)
	}
	return t.output, t.err
}

func fakeTool(name string) *fixtureTool {
	return &fixtureTool{definition: openrouter.Function{Name: name, Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
	}}, output: "ok"}
}

func toolCall(name, args string) openrouter.ToolCall {
	return openrouter.ToolCall{ID: "call_test", Type: "function", Function: openrouter.FunctionCall{Name: name, Arguments: args}}
}

func TestRegistryFreezesDefinitionsAndRejectsDuplicateRegistration(t *testing.T) {
	var registry Registry
	second, first := fakeTool("second"), fakeTool("first")
	for _, tool := range []*fixtureTool{second, first} {
		if err := registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Register(first); err == nil {
		t.Fatal("duplicate tool replaced its implementation")
	}
	if err := registry.Register(nil); err == nil {
		t.Fatal("nil tool accepted")
	}
	broken := fakeTool("")
	if err := registry.Register(broken); err == nil {
		t.Fatal("unnamed tool accepted")
	}
	first.definition.Parameters["properties"].(map[string]any)["path"] = "changed"
	defs := registry.Defs()
	if len(defs) != 2 || defs[0].Function.Name != "first" || defs[1].Function.Name != "second" || defs[0].Type != "function" {
		t.Fatal(defs)
	}
	properties := defs[0].Function.Parameters["properties"].(map[string]any)
	if properties["path"] == "changed" {
		t.Fatal("registration retained mutable caller schema")
	}
	properties["path"] = "changed again"
	if registry.Defs()[0].Function.Parameters["properties"].(map[string]any)["path"] == "changed again" {
		t.Fatal("Defs exposed registry schema")
	}
}

func TestRegistryErrorsRemainBoundedTextAndDoNotExecuteInvalidCalls(t *testing.T) {
	var registry Registry
	tool := fakeTool("demo")
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range []string{"", "null", "[]", `"value"`, "1", `{bad`, `{} {}`, string([]byte{0xff}), strings.Repeat(" ", MaxArgumentBytes+1)} {
		if output := registry.Run(context.Background(), toolCall("demo", arguments)); !strings.HasPrefix(output, "error: ") {
			t.Fatalf("accepted %q: %s", arguments, output)
		}
	}
	if output := registry.Run(context.Background(), toolCall("missing", "{}")); !strings.Contains(output, "unknown tool") {
		t.Fatal(output)
	}
	wrong := toolCall("demo", "{}")
	wrong.Type = "server_tool"
	if output := registry.Run(context.Background(), wrong); !strings.HasPrefix(output, "error:") {
		t.Fatal(output)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output := registry.Run(ctx, toolCall("demo", "{}")); !strings.Contains(output, "canceled") {
		t.Fatal(output)
	}
	if tool.calls.Load() != 0 {
		t.Fatal("invalid/canceled call reached tool implementation")
	}
	tool.err = errors.New("read failed")
	if output := registry.Run(context.Background(), toolCall("demo", "{}")); output != "error: read failed" {
		t.Fatal(output)
	}
	tool.err = errors.New(strings.Repeat("problem ", MaxOutputChars))
	if output := registry.Run(context.Background(), toolCall("demo", "{}")); utf8.RuneCountInString(output) != MaxOutputChars || !strings.HasSuffix(output, "[truncated]") {
		t.Fatal("error output was not bounded")
	}
}

func TestRegistryRefusesApprovalToolsWithoutCallingThem(t *testing.T) {
	var registry Registry
	tool := fakeTool("command")
	tool.approval = true
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	if output := registry.Run(context.Background(), toolCall("command", "{}")); !strings.Contains(output, "approval is required") || tool.calls.Load() != 0 {
		t.Fatal(output)
	}
}

func TestRegistryCapsUnicodeOutputWithoutBreakingEncoding(t *testing.T) {
	var registry Registry
	tool := fakeTool("demo")
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	tool.output = strings.Repeat("界", MaxOutputChars)
	if output := registry.Run(context.Background(), toolCall("demo", "{}")); output != tool.output {
		t.Fatal("exact character boundary was truncated")
	}
	tool.output += "🙂"
	if output := registry.Run(context.Background(), toolCall("demo", "{}")); !utf8.ValidString(output) || utf8.RuneCountInString(output) != MaxOutputChars || !strings.HasSuffix(output, "[truncated]") {
		t.Fatal("Unicode output was not safely capped")
	}
	tool.output = string([]byte{'a', 0xff, 'b'})
	if output := registry.Run(context.Background(), toolCall("demo", "{}")); !utf8.ValidString(output) {
		t.Fatal("invalid output encoding escaped")
	}
}

func TestRegistrySupportsConcurrentRunDefsAndRegistration(t *testing.T) {
	var registry Registry
	if err := registry.Register(fakeTool("base")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			if err := registry.Register(fakeTool(fmt.Sprint("tool", i))); err != nil {
				t.Error(err)
			}
			for range 20 {
				registry.Defs()
				if result := registry.Run(context.Background(), toolCall("base", "{}")); result != "ok" {
					t.Error(result)
				}
			}
		})
	}
	wg.Wait()
}

func TestRegistryDoesNotReturnSuccessfulOutputAfterCancellation(t *testing.T) {
	var registry Registry
	tool := fakeTool("demo")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool.run = func(context.Context) (string, error) { cancel(); return "must not return after cancel", nil }
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	if output := registry.Run(ctx, toolCall("demo", "{}")); !strings.Contains(output, "canceled") || strings.Contains(output, "must not return") {
		t.Fatal(output)
	}
}
