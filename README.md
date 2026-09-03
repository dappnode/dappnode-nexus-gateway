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
[model catalog](https://nexus.dappnode.com/models).

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
