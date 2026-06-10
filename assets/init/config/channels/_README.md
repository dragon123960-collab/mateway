# Channel Configs

`feishu.sample.yaml` and `weixin.sample.yaml` are safe to keep as user-facing templates.

Copy a sample to its runtime YAML, then enable the channel only after credentials or tokens are configured.

Recommended:

- Keep direct secret fields empty.
- Put real secrets in `../mateway.env`.
- Use `*_env` fields in channel YAML files.
