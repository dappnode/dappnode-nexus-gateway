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

The gateway protects callers from a provider that keeps failing. After five
provider failures in a row (outages, timeouts, server errors or a broken
provider account; never a rejected request, a rate limit or a canceled
client), it stops sending to that provider for 30 seconds: requests go
straight to the model's fallback provider, or, without one, fail at once with
503 `provider_unavailable` and a `Retry-After` header. One request then
probes the provider; a failed probe doubles the pause, up to five minutes.
Without a fallback, a connection error or a 502/503 that the provider returned
within five seconds and before any output is retried once. A request reaches
at most two provider attempts. `PROVIDER_BREAKER_FAILURES` (0 turns the
breaker off), `PROVIDER_BREAKER_OPEN_SECONDS`, `PROVIDER_BREAKER_MAX_OPEN_SECONDS`
and `PROVIDER_RETRY_FAST_FAILURES` change these defaults.

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
