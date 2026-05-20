# Mateway 配置样例

这一组文件对应运行时默认结构：

```text
~/.mateway/config/
  config.yaml
  models/
    minimax.yaml
    local-mlx.yaml
```

当前建议：

- `models/*.yaml` 只声明模型端点、API 类型、模型名、密钥来源。
- `config.yaml` 的顶层 `model.default` 决定单 agent 兼容默认模型。
- `agents.profiles[].model.default` 决定某个 agent 的默认模型。
- `fallbacks` 只定义降级顺序，不会自动抢占默认模型。
- 本地 `mlx_lm` 可以启用，但不会因为 `api: openai` 自动成为默认模型。

如果要让本地模型成为默认模型，把下面两处从 `minimax` 改成 `local-mlx`：

```yaml
model:
  default: local-mlx

agents:
  profiles:
    - id: main
      model:
        default: local-mlx
```
