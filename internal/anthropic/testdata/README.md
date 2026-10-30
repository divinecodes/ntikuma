# Anthropic Messages fixtures

Written by hand on 2026-10-30 from the documented Messages API stream
(`message_start`, `content_block_start`/`_delta`/`_stop`, `message_delta`,
`message_stop`, `ping`), not captured from a live response. Replace them with
sanitized captures after the live checks.

- `text-stream.sse`: a thinking block with an empty text and a signature (the
  default `display: "omitted"` on current models), then a text answer.
- `tool-stream.sse`: thinking, a short text and a `tool_use` block whose input
  arrives as `input_json_delta` fragments, ending with `stop_reason: "tool_use"`.

Signatures are placeholders; the API's are opaque and sent back unchanged.
