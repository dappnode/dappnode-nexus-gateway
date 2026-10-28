# Dappnode Nexus Gateway

The open-source API gateway behind [Dappnode Nexus](https://nexus.dappnode.com),
a unified, OpenAI-compatible API for accessing multiple AI models.

[Open Nexus](https://nexus.dappnode.com) ·
[Browse models](https://nexus.dappnode.com/models) ·
[Get help](https://nexus.dappnode.com/help)

## Get started

1. Sign in to [Nexus](https://nexus.dappnode.com).
2. Choose a subscription or add credit from **Billing**.
3. Create a key from **API Keys** and store it securely. The key is shown only
   once.
4. Choose a model ID from **Models**.

Use the following OpenAI-compatible connection settings:

| Setting | Value |
| --- | --- |
| Base URL | `https://nexus-api.dappnode.com/v1` |
| Authentication | `Authorization: Bearer YOUR_API_KEY` |

## Make a request

Set `NEXUS_API_KEY` in your environment and replace `MODEL_NAME` with a model
from your Nexus dashboard.

```sh
curl https://nexus-api.dappnode.com/v1/chat/completions \
  -H "Authorization: Bearer ${NEXUS_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MODEL_NAME",
    "messages": [
      {"role": "user", "content": "Hello!"}
    ]
  }'
```

### OpenAI Python client

```sh
pip install openai
```

```python
import os

from openai import OpenAI

client = OpenAI(
    api_key=os.environ["NEXUS_API_KEY"],
    base_url="https://nexus-api.dappnode.com/v1",
)

response = client.chat.completions.create(
    model="MODEL_NAME",
    messages=[{"role": "user", "content": "Hello!"}],
)

print(response.choices[0].message.content)
```

## API compatibility

| Endpoint | Description |
| --- | --- |
| `GET /v1/models` | Lists the available models, prices and capabilities. |
| `POST /v1/chat/completions` | Creates chat completions; supports streaming. |

Supported capabilities depend on the selected model and can include streaming,
tool calls, parallel tool calls, structured outputs and reasoning. The current
capabilities for each model are shown in the
[model catalog](https://nexus.dappnode.com/models). `GET /v1/models` lists
them per model in `capabilities`, using models.dev field names (`modalities`,
`tool_call`, `structured_output`, `reasoning`, `reasoning_options`, …); a
missing field means not stated. They describe each model
and are not checked against chat requests: the selected provider validates chat
options, so a stale catalog flag cannot reject a request the provider would
accept. Endpoint routing, privacy policy and credit checks still apply.

Provider errors keep their meaning. A request the provider rejects keeps its
HTTP status (for example 400 or 422) with `provider_rejected_request`. A
provider credential, billing or permission failure becomes 502
`provider_configuration_error`, because it concerns Nexus's provider account,
not the caller's key. Provider rate limits become `provider_rate_limited` and
keep a valid `Retry-After` header. Errors include `origin` (`gateway` or
`upstream`) and, when known, the numeric `upstream_status`; other diagnostic
details are not exposed. A configured fallback provider is tried only after a
rate limit or a server-side failure, never after a rejected request.

Responses have no limit on total duration. Two stall guards apply instead:
`PROVIDER_SILENCE_TIMEOUT_SECONDS` (default 900) ends a request when the
provider sends nothing, neither headers nor bytes, for that long, and
`CLIENT_IO_TIMEOUT_SECONDS` (default 60) ends one when a single read from or
write to the client is blocked for that long. Both must be positive whole
seconds.

## Privacy and usage controls

Nexus provides per-key PII masking settings and access to privacy-focused
models. Configure these options from **API Keys**, and monitor requests, tokens
and costs from **Usage**. See the [Privacy Policy](https://nexus.dappnode.com/privacy)
for details about how Nexus processes data.

## Help and feedback

- Read the [Nexus help guide](https://nexus.dappnode.com/help).
- Join the [Dappnode Discord community](https://discord.gg/dappnode).
- Report a problem through [GitHub Issues](https://github.com/dappnode/dappnode-nexus-gateway/issues).

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the
development workflow.

## License

Licensed under [Apache-2.0](LICENSE). Bundled dependency notices are available
in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Operator security notes

Provider `api_key_secret_ref` values must name uppercase environment variables
ending in `_API_KEY` (for example `TINFOIL_API_KEY`). Other references fail closed;
internal credentials such as `METERING_TOKEN` cannot be used as provider keys.

The Nitro confidential endpoint authenticates before storing replay IDs. Each
account may retain at most 10,000 IDs during the three-minute replay window,
shared across its API keys; excess requests return 429 with
`confidential_request_limit_exceeded`. The process-wide limit remains 100,000.
Live entries are never evicted to admit new requests. Metrics bind to loopback
in Nitro builds and retain their internal-network listener in ordinary builds.

PII masking covers conversation text, tool results, assistant reasoning, tool-call
argument string values, and `user`. Tool definitions, names, IDs, schemas,
metadata, provider options, and image/audio content are not scanned. Keep
personal data out of those fields even when masking is enabled.

Guest chat keys (accounts flagged `is_guest` by Metering) are narrowed by
`services.GuestPolicy`: only the models in `GUEST_MODELS` (default
`deepseek/deepseek-v4.1-flash`), requests up to `GUEST_MAX_REQUEST_BYTES`
(default 16384), output capped at `GUEST_MAX_OUTPUT_TOKENS` (default 1024) and
written into the request, and only plain chat fields with text content. The
reservation for a guest request is bounded by the bytes sent upstream rather
than the model's context window, so it fits a guest's small credit. Invalid
values stop the gateway at startup. The Nexus repository's
`docs/guest-chat.md` describes the whole design.
