# OpenRouter response fixtures

## Web server tools, Phase 13

Captured on 2026-10-26 with curl against OpenRouter Chat Completions, using the
configured Sonnet/GPT fallback chain, automatic engines, five search results,
three fetches, and six total tool calls. Both responses used
`anthropic/claude-sonnet-5.5` and reported one search.

- `web-stream.sse`: tool events arrived while working, but 80 answer-text deltas
  arrived together between 17.300 and 17.313 seconds. Citations were in
  `choices[].delta.annotations`; usage was in the final accounting chunk.
- `web-completion.json`: the non-streaming comparison completed in 14.699 seconds,
  with citations in `choices[].message.annotations`.

The actual usage field was `usage.server_tool_use_details.web_search_requests`.
The client also accepts the documented `usage.server_tool_use` spelling.

Sanitization removes generation IDs, timestamps, provider details, reasoning,
internal tool-call events, citation excerpts, and unrelated token/cost fields.
It retains answer text, citation URLs/titles, finish reasons, and search usage.
Credentials were supplied to curl through stdin and never included in captures.
Raw captures and timings remain private local review artifacts.

## Client function tools, Phase 14A

Six bounded curl requests ran on 2026-10-26: a request and manually constructed
continuation for function-only SSE, function-only JSON, and mixed Web/function
JSON. The configured Sonnet/GPT chain resolved to `anthropic/claude-sonnet-5`
for the first two tool requests and `anthropic/claude-sonnet-5.5` for their
continuations and the mixed pair. These observations describe that chain/date;
they do not establish behavior for every provider or fallback model.

The dummy `get_demo_fact` function never read files or ran commands. Its result
was a synthetic confirmation code. Each continuation included the returned
assistant tool call and a `role: "tool"` reply with the matching ID. Function
requests used `provider.require_parameters: true`.

| Capture | Observed behavior |
| --- | --- |
| `function-stream.sse` | First delta carried index, ID, type and function name. Three nonempty argument fragments followed. `finish_reason: "tool_calls"` appeared twice before `[DONE]`. |
| `function-stream-followup.sse` | The follow-up streamed the confirmation code and finished with `stop`. |
| `function-completion.json` | Tool call in `message.tool_calls`, with `content: null`. |
| `function-completion-followup.json` | The follow-up answered with the confirmation code. |
| `mixed-tools-completion.json` | Client function call plus signed `reasoning_details`; no search usage was reported in this response. |
| `mixed-tools-completion-followup.json` | Final answer, citations, signed reasoning state, and one reported web search in `server_tool_use_details`. |

The mixed request advertised web search, web fetch and the client function in
the same array. The observed search occurred during the continuation, not
before the client call as the test prompt requested. Web fetch was available
but its execution was not separately established. Answer freshness/correctness
was not evaluated by this protocol test.

Sanitization retains delta boundaries, null content, tool arguments, finish
reasons, model names, citation URLs/titles and reported search counts. Generation
IDs, timestamps, provider/account data and unrelated token/cost fields are
removed. Tool IDs are replaced with demo IDs. Reasoning text and signatures are
replaced with explicit placeholders; those fixtures cannot be replayed against
the live provider. The live continuation used the original state in memory.
Credentials went through curl stdin and were never written to captures.

Additional synthetic cases in `tool_calls_test.go` cover interleaved calls,
empty-object arguments, unknown reasoning fields, mixed screenshot/tool message
serialization, malformed and interrupted streams, conflicts, cancellation and
response budgets. Reasoning blocks stay in wire order and never become answer
text. Invalid argument JSON remains untrusted text for the later registry to
reject as a recoverable tool error; malformed transport/envelopes fail the
client and expose no completed calls.

When structured reasoning details are present, request serialization uses them
instead of duplicating the plain reasoning field. Plain reasoning is retained
as a fallback for providers that do not return structured details.

Client transport limits are 64 calls per response, 64 KiB of arguments per call,
256 KiB of arguments in total, and 2 MiB of reasoning continuation state. These
are parser limits, separate from the tighter execution budgets planned for the
chat loop. Local execution and approval controls are not enabled by Phase 14A.
