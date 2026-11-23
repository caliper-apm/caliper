# Caliper APM

**Native 优先、React Native 一等兼容、统计零谎言（Evidence-First）的开源移动端 APM 平台。**

[English](README.md) · [北极星宪章](NORTH_STAR.md) · [Zero-Lie 统计契约](spec/metrics-contract.md) · [更新日志](CHANGELOG.md) · [开销评测报告](benchmarks/REPORT.md)

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

## 生产级集群与工程化能力（v0.5）

详见 [`docs/roadmap-v0.5.md`](docs/roadmap-v0.5.md)：
- **ClickHouse 生产存储后端**：零 CGO 原生 HTTP 协议驱动（`--storage-driver=clickhouse`），基于分区 `ReplacingMergeTree` 与 `sumForEach` 向量化直方图合并，支持 `docker compose --profile cluster up` 一键拉起集群模式。
- **统计显著性回归检测引擎**：比例指标采用双比例 Z 检验 + Wilson 95% 置信区间，耗时/内存直方图采用双样本 Kolmogorov-Smirnov（KS）检验，内置 `POST /v1/analytics/regressions/scan` 分场景自动归因排序。
- **RBAC 权限控制与强制审计的 UID/Session 排查**：支持 `--auth-config` 四级角色（`ingest_only` / `viewer` / `operator` / `admin`）与 App 作用域隔离；单用户问题排查（`GET /v1/sessions/lookup`）仅接受非 PII 十六进制哈希且强制填写工单原因，Fail-Closed 写入不可篡改审计日志（`GET /v1/audit/logs`）。
- **零手工运维 CI 构建插件**：内置 Android Gradle 插件（`packages/gradle-plugin`，`io.caliper.apm.symbols`）、iOS Fastlane 插件（`packages/fastlane-plugin`）与复合 GitHub Action（`packages/github-action`）。

## 深度诊断与 OpenTelemetry 互通（v1.0）

详见 [`docs/roadmap-v1.0.md`](docs/roadmap-v1.0.md)：
- **在线 Native 与 Hermes CPU 火焰图**：有界折叠调用栈采集（`cpu_profile`），服务端自动通过 R8 / SourceMap v3 / iOS dSYM 符号化，Web 控制台内置可点击缩放的交互式火焰图。
- **裁剪式堆内存 / OOM 深度诊断**：零 PII 类级别 Dominator 摘要（`heap_snapshot`），支持跨版本内存增量对比与自动泄漏嫌疑识别（`POST /v1/diagnostics/heap/compare`）。
- **强审计远程日志捞取任务**：支持按 `user_hash` / `install_hash` 下发带 TTL 与字节上限的远程日志拉取任务，SDK 轮询 + Gzip 加密摘要回传，全链路 Fail-Closed 写入审计日志。
- **全双工 OTLP 互通**：原生接收标准 OpenTelemetry Traces / Logs（`POST /v1/otlp/v1/traces`、`/v1/otlp/v1/logs`），并支持以标准 `resourceMetrics` 导出聚合指标（`GET /v1/otlp/export`）。

## 企业级控制台 2.3、生产 SDK 与 CI 质量门禁（v1.1 – v1.4）

详见 [`CHANGELOG.md`](CHANGELOG.md)：
- **Console 2.3 企业级多维可视化**：中英双语、14 天版本放量趋势标注、**按版本 / 机型 / 系统版本 / Native vs RN** 多维切换拆解、19 桶分布直方图、5 域网络失败占比、HTTP 状态码分布、**慢请求/异常请求单次瀑布样本抽屉**、**核心业务链路自定义多阶段追踪（`GET /v1/analytics/journeys`）**、跨页下钻与可视化单用户会话时间线。
- **CI/CD 灰度质量门禁（`caliper-gate` CLI + GitHub PR Comment Action）**：基于 Z 检验 + KS 检验 + 符号覆盖率的零谎言发版红绿灯（`POST /v1/analytics/release-gate`）及一键 PR 评论 Action（`packages/github-action/release-gate/action.yml`），退出码 `0`（PASS）、`2`（BLOCK 显著劣化）、`3`（GATED 样本不足）：
  ```bash
  caliper-gate --server http://127.0.0.1:8080 --platform android --baseline 1.0.0 --candidate 1.1.0 --format markdown
  ```
- **稳定性异常流转与影响分布**：异常全生命周期处置（待处理 / 处理中 / 已修复 / 已忽略、负责人、修复版本、备注 + 审计留痕）、单异常的机型/系统/版本 ECharts 分布图，以及抽屉内一键跳转单点追查。
- **远程动态采样策略与运维自检**：控制台热更新采样率（% / PPM）、单设备 24h 上限、租约 TTL 与各探针开关（`GET/PUT /v1/policy`），配套告警规则一键测通/启停/删除、数据源健康自检（`GET /v1/ops/source-health`）与 Prometheus `GET /metrics`。
- **生产端 SDK 深度与集群高可用**：Android/iOS 离线落盘队列与主线程 Watchdog、Android `CaliperAndroidAgent`（`FrameMetrics` / `ApplicationExitInfo` / `PSS`）、iOS `CaliperMetricKitBridge`、RN 原生桥、纯 Go AWS SigV4 S3/MinIO 对象存储（`--object-store=s3`）、ClickHouse 机型/系统切片、PostgreSQL 元数据 DDL 与 Kubernetes Helm Chart（`deploy/helm/caliper-apm`）。

---

## 开源协议

基于 [Apache License 2.0](LICENSE) 开源。
