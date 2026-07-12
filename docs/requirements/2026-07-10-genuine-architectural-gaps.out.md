证据全部属实。文件行号略有偏移（relay.go 的背景上下文实际在第 163/170 行而非 ~130-140），但定位准确。

---

### 建议下一步

既然您已完成分析，我建议选一个方向开始落地原型。按您推荐的启动顺序：

**方向 1（异步管道追踪）** 入门门槛最低且收益立即可见。我建议做以下几件事验证可行性：

1. **修改 outbox Event 结构**，在 payload 中嵌入 W3C `traceparent`
2. **relay drain() 反序列化 traceparent**，创建子 span 挂回原 trace
3. **SSE publish 接入 tracer**，使推送链路可追踪

规模约 80-120 行新代码，不影响现有测试。完成后可直接验证：

```bash
# 验证 trace 从 HTTP → outbox → relay → SSE 的传播
AERO_ID_OTLP_ENDPOINT=localhost:4317 go run ./cmd/server -config configs/config.yaml
# 发一条消息，看 Jaeger 中是否出现完整 causal chain
```

您希望我从哪个方向开始实现？还是您想先讨论某个方向的方案细节？
