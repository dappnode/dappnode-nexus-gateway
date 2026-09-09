# Streaming usage retention and diagnostics

The handler drains trailing usage after a completion event and emits
`finish_reason` and `[DONE]` after upstream EOF. Usage attached to text, role,
tool, reasoning, or empty deltas is propagated to metering. The finalization
wait has a five-second bound; it also includes the metering call triggered at EOF.

## Finding an affected request

These events were historically logged as `generation completed`, even when
usage was nil. That message indicated generation success, not a successful
credit deduction. Historical logs cannot reconstruct missing token counts.

New diagnostics work at the default `LOG_LEVEL=info`:

| Message | Level | What it establishes |
| --- | --- | --- |
| `provider stream ended` | Info, or Warn for missing usage/parser/transport issues | OpenAI-compatible raw stream termination and usage mapping counts |
| `stream completed without usage` | Warn | The usage tracker has no token counts; includes request, reservation, provider response, and account IDs |
| `stream interrupted after finish` | Warn | A read error was classified as generation success because a finish reason had arrived |
| `stream error` | Error | Failure before completion, including known usage and correlation IDs |
| `generation completed` | Info | Generation outcome, `usage_received`, token counts, and tracker `stream_end` |
| `stream metering completed` | Info | Metering accepted the report; **does not prove a nonzero credit deduction** |
| `failed to record stream usage` | Error | Metering did not acknowledge the report |
| `stream finalization timeout` | Warn | The five-second finalization timer fired; may involve the upstream drain or metering latency |
| `stream client write failed` | Warn | Writing a downstream chunk or `[DONE]` failed |

For OpenAI-compatible providers, `upstream_end` distinguishes `done_marker`,
`eof`, `canceled`, `deadline_exceeded`, `read_error`, `provider_error`, and `closed`.
`chunks_received`, `malformed_chunks`, `unsupported_data_lines`, `usage_chunks`,
and `mapped_usage_chunks` distinguish missing upstream data from parsing loss.
The tracker’s `stream_end=eof` alone cannot distinguish upstream `[DONE]` from
bare EOF; use the correlated provider summary.

Only a stream reaching `[DONE]` with no usage and no parsing/framing anomalies
supports provider omission. Cancellation or bare EOF does not establish omission.
The provider summary is attached to the generic OpenAI adapter and Tinfoil's
OpenAI parser. Other adapters still have tracker/metering diagnostics.

With `LOG_LEVEL=debug`, `provider stream chunk metadata` records each chunk
containing usage or a finish reason: chunk index, tool-call presence, choices
count, usage received/forwarded, and token counts. `stream completion sent`
records the downstream terminal-write result. No new diagnostic logs contain
raw SSE, prompts, generated content, tool arguments, or credentials.

Application-side log sampling is disabled so bursts do not silently drop
per-request diagnostics. Log collection and retention still depend on deployment.

Example Loki queries using the existing project label:

```logql
{compose_project="nexus"} | json | msg="stream completed without usage"
```

```logql
{compose_project="nexus"} | json | request_id="YOUR_GATEWAY_REQUEST_ID"
```

```logql
{compose_project="nexus"} | json | provider_request_id="YOUR_PROVIDER_RESPONSE_ID"
```

Search gateway logs, not only control-plane logs. A finalization timeout with
`upstream_done_seen=true` and known usage points toward settlement latency;
`upstream_end=canceled` without usage points toward an interrupted upstream drain.
These are diagnostic clues, not evidence that a provider charged a specific amount.

## Verification and limits

The local tests cover normal usage, provider omission, malformed and unsupported
SSE, cancellation, deadline expiry, usage on tool deltas, and metering failure.
They assert correlation fields, severity, no content leakage, and unchanged
accounting outcomes. The streaming regression still exercises real local HTTP
connections. No paid provider calls are needed.

This change adds observability to the staged stream fix. It does not introduce
new charging policies, durable settlement delivery, estimated charges, or a way
to recover historical missing usage.

Privacy: request summaries retain only fixed metadata keys, numeric/boolean settings,
and counts. Tool names, stop strings, schemas and arbitrary stream options are omitted.
Generation and HTTP error logs omit error messages and arbitrary upstream metadata,
because providers can echo prompts in errors; numeric upstream status is retained.
Unknown finish reasons are logged as `unknown`. Correlation IDs remain visible;
never put prompts or credentials in request IDs. These changes do not erase historical logs.
