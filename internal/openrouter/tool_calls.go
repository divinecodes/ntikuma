package openrouter

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // Untrusted JSON text; validated by the executing tool.
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

const (
	maxResponseToolCalls      = 64
	maxToolArgumentBytes      = 64 * 1024
	maxTotalToolArgumentBytes = 256 * 1024
	maxReasoningBytes         = 2 * 1024 * 1024
)

// Stream indices are transport fields, not part of a replayed ToolCall.
type toolCallDelta struct {
	Index    *int         `json:"index"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type accumulatedCall struct {
	id, kind, name string
	arguments      strings.Builder
}

type toolCallAccumulator struct {
	calls         map[int]*accumulatedCall
	argumentBytes int
	// lenient accepts what some OpenAI-compatible servers send: streamed
	// calls without indices when they can be told apart, and calls without
	// an ID or type. OpenRouter always sends them.
	lenient bool
	// The first streamed delta decides whether a stream uses indices.
	indexed, unindexed bool
	ids                map[string]int // unindexed streams: call ID → index
}

func (a *toolCallAccumulator) add(deltas []toolCallDelta, streamed bool) error {
	for ordinal, delta := range deltas {
		index := ordinal
		if delta.Index != nil {
			if streamed && a.unindexed {
				return errors.New("openrouter: tool-call stream mixes calls with and without an index")
			}
			index, a.indexed = *delta.Index, a.indexed || streamed
		} else if streamed {
			if !a.lenient || a.indexed {
				return errors.New("openrouter: streamed tool call is missing its index")
			}
			a.unindexed = true
			var err error
			if index, err = a.correlate(delta, len(deltas)); err != nil {
				return err
			}
		}
		if index < 0 {
			return errors.New("openrouter: negative tool-call index")
		}
		if a.calls == nil {
			a.calls = make(map[int]*accumulatedCall)
		}
		call := a.calls[index]
		if call == nil {
			if len(a.calls) >= maxResponseToolCalls {
				return errors.New("openrouter: too many tool calls in one response")
			}
			call = &accumulatedCall{}
			a.calls[index] = call
		}
		for _, header := range []struct {
			dst   *string
			value string
			limit int
		}{
			{&call.id, delta.ID, 512}, {&call.kind, delta.Type, 32}, {&call.name, delta.Function.Name, 256},
		} {
			if len(header.value) > header.limit {
				return errors.New("openrouter: tool-call header is too large")
			}
			if header.value == "" {
				continue
			}
			if *header.dst != "" && *header.dst != header.value {
				return errors.New("openrouter: conflicting tool-call header")
			}
			*header.dst = header.value
		}
		if a.unindexed && call.id != "" {
			if a.ids == nil {
				a.ids = make(map[string]int)
			}
			a.ids[call.id] = index
		}
		arguments := delta.Function.Arguments
		if len(arguments) > maxToolArgumentBytes-call.arguments.Len() || len(arguments) > maxTotalToolArgumentBytes-a.argumentBytes {
			return errors.New("openrouter: tool-call arguments are too large")
		}
		call.arguments.WriteString(arguments)
		a.argumentBytes += len(arguments)
	}
	return nil
}

// correlate finds the call an unindexed delta belongs to: the call with its
// ID, a new call for a new ID or the first delta, or the only open call when
// the chunk has just this one delta. Anything else could join the wrong call.
func (a *toolCallAccumulator) correlate(delta toolCallDelta, inChunk int) (int, error) {
	if delta.ID != "" {
		if index, ok := a.ids[delta.ID]; ok {
			return index, nil
		}
		return len(a.calls), nil
	}
	switch {
	case inChunk > 1:
	case len(a.calls) == 0:
		return 0, nil
	case len(a.calls) == 1:
		for index := range a.calls {
			return index, nil
		}
	}
	return 0, errors.New("openrouter: can't tell which tool call a fragment without an index or ID belongs to")
}

func (a *toolCallAccumulator) finish(reason string) ([]ToolCall, error) {
	if len(a.calls) == 0 {
		if reason == "tool_calls" {
			return nil, errors.New("openrouter: tool-call response contains no calls")
		}
		return nil, nil
	}
	if reason != "tool_calls" {
		return nil, fmt.Errorf("openrouter: tool calls did not complete (finish_reason %q)", reason)
	}
	indices := make([]int, 0, len(a.calls))
	for index := range a.calls {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	result := make([]ToolCall, 0, len(indices))
	ids := make(map[string]bool)
	for _, index := range indices {
		call := a.calls[index]
		if a.lenient && call.kind == "" {
			call.kind = "function"
		}
		if a.lenient && call.id == "" {
			// Once per call, so its tool reply refers to the same ID.
			b := make([]byte, 8)
			if _, err := rand.Read(b); err != nil {
				return nil, err
			}
			call.id = "call_" + hex.EncodeToString(b)
		}
		if call.id == "" || call.name == "" || call.kind != "function" || ids[call.id] {
			return nil, errors.New("openrouter: incomplete or duplicate function-call envelope")
		}
		ids[call.id] = true
		result = append(result, ToolCall{ID: call.id, Type: call.kind, Function: FunctionCall{Name: call.name, Arguments: call.arguments.String()}})
	}
	return result, nil
}

// Some providers include a complete message after streaming its deltas. It
// may confirm the accumulated calls, but must not silently replace their args.
func (a *toolCallAccumulator) snapshot(deltas []toolCallDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	if len(a.calls) == 0 {
		return a.add(deltas, false)
	}
	var full toolCallAccumulator
	if err := full.add(deltas, false); err != nil {
		return err
	}
	if len(full.calls) != len(a.calls) {
		return errors.New("openrouter: conflicting tool-call snapshot")
	}
	for index, call := range a.calls {
		other := full.calls[index]
		if other == nil || call.id != other.id || call.kind != other.kind || call.name != other.name || call.arguments.String() != other.arguments.String() {
			return errors.New("openrouter: conflicting tool-call snapshot")
		}
	}
	return nil
}

// ToolCallAccumulator assembles streamed calls by index for other adapters,
// with the same limits and checks as Chat Completions streams.
type ToolCallAccumulator struct{ calls toolCallAccumulator }

// Add appends a fragment of the call at index; id and name may repeat or be
// empty after the first fragment.
func (a *ToolCallAccumulator) Add(index int, id, name, arguments string) error {
	return a.calls.add([]toolCallDelta{{Index: &index, ID: id, Type: "function", Function: FunctionCall{Name: name, Arguments: arguments}}}, true)
}

// Finish returns the calls in index order once reason is "tool_calls".
func (a *ToolCallAccumulator) Finish(reason string) ([]ToolCall, error) {
	return a.calls.finish(reason)
}
