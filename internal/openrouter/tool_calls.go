package openrouter

import (
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
}

func (a *toolCallAccumulator) add(deltas []toolCallDelta, streamed bool) error {
	for ordinal, delta := range deltas {
		index := ordinal
		if delta.Index != nil {
			index = *delta.Index
		} else if streamed {
			return errors.New("openrouter: streamed tool call is missing its index")
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
		arguments := delta.Function.Arguments
		if len(arguments) > maxToolArgumentBytes-call.arguments.Len() || len(arguments) > maxTotalToolArgumentBytes-a.argumentBytes {
			return errors.New("openrouter: tool-call arguments are too large")
		}
		call.arguments.WriteString(arguments)
		a.argumentBytes += len(arguments)
	}
	return nil
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
