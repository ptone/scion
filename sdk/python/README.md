# Scion Python SDK

Python client library for the Scion Hub API.

## Installation

```bash
pip install scion-sdk
```

## Quick Start

```python
from scion import ScionClient

client = ScionClient("https://hub.example.com", token="your-token")
health = client.health()
print(health.status)
```

## License

Apache License 2.0
