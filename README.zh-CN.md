# Caliper APM

**Native 优先、React Native 一等兼容、统计零谎言（Evidence-First）的开源移动端 APM 平台。**

[English](README.md) · [北极星宪章](NORTH_STAR.md) · [Zero-Lie 统计契约](spec/metrics-contract.md) · [v0.1 路线图](docs/roadmap-v0.1.md) · [开销评测报告](benchmarks/REPORT.md)

---

## 为什么做 Caliper APM？

绝大多数中大型移动 App 采用 **Native 承载核心高体验链路（首页 Feed、音视频、冷启动、底层网络）+ React Native 承载高频迭代业务页** 的混合架构。现有开源与商业方案普遍存在断层：
- 纯客户端诊断库（如 Matrix/KOOM）缺少服务端、跨版本对比、符号化与告警闭环；
- 重型通用可观测平台自托管门槛高，对移动端流畅度、冷启动分阶段、低开销有界采样与统计严谨性支持不足；
- 商业闭源 APM 黑盒昂贵，统计口径不透明。

**Caliper APM** 的五大核心特性：
1. **Native 优先内核 + RN 一等适配**：`sdk-android`（Kotlin）与 `sdk-ios`（Swift SPM）**零 RN 依赖**；混合 App 挂接 `sdk-react-native` 后，自动与原生生命周期、窗口关联与符号化时间线合二为一。
2. **Zero-Lie 统计引擎**：19 桶固定边界直方图可合并分位数（绝不平均每日 P90）、缺值绝不补零、零帧窗口绝不误判为 0% 卡顿、Android `deadline` 与 `heuristic` 算法严格隔离、版本对比强制样本量门禁（`n >= 100`）。
3. **内置多格式自动符号化**：纯 Go 实现 **R8/ProGuard Retrace**、**Source Map v3 VLQ（JS/Hermes）**、**Breakpad/JSON dSYM** 自动还原，直接将崩溃堆栈与流畅度同窗卡顿栈还原到精确源码行。
4. **真告警与多渠道 Webhook**：样本量门禁防误报 + 冷却去重 + 恢复自动通知，原生支持 **飞书交互卡片、Slack Block Kit、Discord Embed、通用 JSON**。
5. **单二进制自托管 + Chaos Lab 故障实验室**：单 Go 二进制同时托管采集/分析 API 与 5 页真数据 Web 控制台，自带 **Atlas Reader** 混合 Demo 与一键故障注入实验室。

---

## 快速体验（1 分钟内）

### 方式一：Docker Compose（推荐）
```bash
git clone https://github.com/<your-org>/caliper-apm.git
cd caliper-apm
docker compose up --build
# 打开 http://localhost:8080 即可查看预置的 v1.0.0 -> v1.1.0（回归）-> v1.2.0（修复）完整闭环
```

### 方式二：本地零 Docker 一键启动（需 Go 1.23+ 与 Node 22+）
```bash
./scripts/dev-quickstart.sh
# 打开 http://127.0.0.1:8080/
```

### 运行实时 Chaos Lab 端到端验收（< 3 秒）
自动上传 R8 / Hermes SourceMap v3 / iOS dSYM 符号包、向 `mock-api` 注入真实 502 风暴与三端混淆故障、断言源码行级符号化与飞书+Slack 真实 Webhook 投递：
```bash
./demo-app/chaos-lab/run-chaos-e2e.sh
```

---

## 实测宿主机性能基准（2026-11-20）

完整指标与物理真机 Release 门禁追踪见 [`benchmarks/REPORT.md`](benchmarks/REPORT.md)，可通过 `./benchmarks/run-overhead-bench.sh` 一键复现。

| 组件 | 压测场景 | 实测结果 |
|---|---|---|
| Go 内嵌存储引擎 | 100 事件批次写入 | **104.3 µs / 100 条**（进程内 ~95.8 万 events/s） |
| 真实 HTTP + WAL 回放 | 99,996 条事件（504 个 Gzip 批次） | **2.98 秒**（**33,566 events/s**）；跨版本对比查询 **0.34–0.90 ms** |
| RN 适配器 OFF vs ON | 10 万次 API + 1 万次 LongTask | 单次 API 开销 **42 ns**；**550× 过桥流量压缩** |
| Chaos Lab 全链路 E2E | 上传三端符号 + 三端自动符号化 + 双 Webhook 投递 | 全流程 **2.74 秒** |

---

## 开源协议

基于 [Apache License 2.0](LICENSE) 开源。
