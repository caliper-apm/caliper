# Caliper APM

**Native 优先、React Native 一等兼容、统计零谎言（Evidence-First）的开源移动端 APM 平台。**

[English](README.md) · [系统架构](docs/architecture.md) · [自托管部署](docs/self-hosting.md) · [Android SDK](docs/sdk-android.md) · [iOS SDK](docs/sdk-ios.md) · [RN 适配层](docs/sdk-react-native.md) · [评测报告](benchmarks/REPORT.md) · [更新日志](CHANGELOG.md)

---

## 为什么做 Caliper APM？

绝大多数中大型移动 App 采用 **Native 承载核心高体验链路（首页 Feed、音视频、冷启动、底层网络）+ React Native 承载高频迭代业务页** 的混合架构。现有开源与商业方案普遍存在断层：
- 纯客户端诊断库（如 Matrix/KOOM）缺少服务端、跨版本对比、符号化与告警闭环；
- 重型通用可观测平台自托管门槛高，对移动端流畅度、冷启动分阶段、低开销有界采样与统计严谨性支持不足；
- 商业闭源 APM 黑盒昂贵，统计口径不透明。

**Caliper APM** 的五大核心特性：
1. **Native 优先内核 + RN 一等适配**：`sdk-android`（Kotlin + NDK）与 `sdk-ios`（Swift SPM + C/Mach）**零 RN 依赖**；混合 App 挂接 `sdk-react-native` 后，自动与原生生命周期、窗口关联、W3C `traceparent` 与符号化时间线合二为一。
2. **Zero-Lie 统计引擎**：内置纯 Go 零依赖 **DDSketch（$\alpha = 2\%$）** 可合并分位数引擎（精确输出 `P50 / P90 / P95 / P99` 与 **`DDSketchKSTest`** 桶内劣化检测，零破坏兼容 19 桶直方图线性插值，绝不平均每日 P90）、缺值绝不补零、零帧窗口绝不误判为 0% 卡顿、Android `deadline` 与 `heuristic` 严格隔离、版本对比强制样本量门禁（`n >= 100`）。
3. **内置多格式自动符号化**：纯 Go 实现 **R8/ProGuard Retrace**、**Source Map v3 VLQ（JS/Hermes）**、**原生 Mach-O `.dSYM`/`.dSYM.zip` 与 Android NDK ELF `.so` DWARF 解析（含内联栈展开、纯 Go Swift 5+ Demangler 与 Itanium C++ Demangle）** 以及 Breakpad `.sym` 自动还原，直接将崩溃堆栈与流畅度同窗卡顿栈还原到精确源码行。
4. **真告警与多渠道 Webhook**：样本量门禁防误报（支持 `P95`/`P99` 长尾指标告警）+ 冷却去重 + 恢复自动通知，原生支持 **飞书交互卡片、Slack Block Kit、Discord Embed、通用 JSON**。
5. **单二进制自托管 + Chaos Lab 故障实验室**：单 Go 二进制同时托管采集/分析 API 与 5 页真数据 Web 控制台，自带 **Atlas Reader** 混合 Demo 与一键故障注入实验室。

---

## 快速体验（1 分钟内）

### 方式一：Docker Compose（推荐）
```bash
git clone https://github.com/caliper-apm/caliper.git
cd caliper
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

## 实测性能基准（宿主机 + 双端物理真机 · 2026-11-23）

完整测试方法、样本分布与 North Star 门禁详见 [`benchmarks/REPORT.md`](benchmarks/REPORT.md)，可通过 `./benchmarks/run-overhead-bench.sh` 与 `./benchmarks/collect-device-bench.sh` 复现。

| 平台 / 组件 | 压测场景（Release OFF vs ON） | 实测结果 |
|---|---|---|
| **Android 真机（`vivo V2343A`，API 34，AOT）** | 20 轮冷启 + 15 轮滑动 + PSS + APK 增量 | 冷启 **`+6.0 ms` P50（`-1 ms` P95）** · 掉帧 **`+0.00%`** · PSS **`+2.67 MiB`** · APK **`+28.0 KiB`** · 自崩溃 **`0`** |
| **iOS 真机（`iPhone 13 Pro Max`，iOS 27.2.1，`-O`）** | 18 轮冷启 + 3×3s ProMotion 滑动 + `phys_footprint` + Mach-O | 冷启 **`+3.98 ms` P50（`+1.51 ms` P90）** · 掉帧 **`+0.00%`** · 内存 **`+0.34 MiB`** · CPU **`+0.10%`** · 体积 **`+247.5 KiB`** |
| **Go 内嵌引擎 + 真实 HTTP/WAL 回放** | 100 条写入 / 99,996 条 Gzip 回放 | **104.3 µs / 100 条** · **33,566–76,904 events/s** · 跨版本对比 **0.17–0.90 ms** |
| **RN 适配器 OFF vs ON** | 10 万次 API + 1 万次 LongTask | 单次 API 开销 **42 ns** · **550× 过桥流量压缩** |
| **Chaos Lab 全链路 E2E** | 三端 R8/SourceMap/dSYM 符号化 + 飞书/Slack 真 Webhook | 全流程 **2.74–2.96 秒** |

---

## 生产级集群与工程化能力（v0.5）

详见 [`docs/roadmap-v0.5.md`](docs/roadmap-v0.5.md)：
- **ClickHouse 生产存储后端**：零 CGO 原生 HTTP 协议驱动（`--storage-driver=clickhouse`），基于分区 `ReplacingMergeTree`、`sumForEach` 向量化直方图合并以及 `sumMapIf` 原生 **DDSketch（±2%）持久化**，消除单机与集群模式精度割裂（`docker compose --profile cluster up`）。
- **统计显著性回归检测引擎**：API 比例指标采用双比例 Z 检验 + Wilson 95% 置信区间，流畅度卡顿率采用**窗口聚类 Z 检验（`cluster_proportion_z`）**避免帧级自相关误报，耗时/内存采用 **`DDSketchKSTest`（`ddsketch_ks`）** + 19 桶回退，分场景批量扫描（`POST /v1/analytics/regressions/scan`）内置 **Benjamini-Hochberg FDR** 多重检验校正。
- **RBAC 权限控制与强制审计的 UID/Session 排查**：支持 `--auth-config` 四级角色（`ingest_only` / `viewer` / `operator` / `admin`）与 App 作用域隔离；单用户问题排查（`GET /v1/sessions/lookup`）仅接受非 PII 十六进制哈希且强制填写工单原因，Fail-Closed 写入不可篡改审计日志（`GET /v1/audit/logs`）。
- **零手工运维 CI 构建插件**：内置 Android Gradle 插件（`packages/gradle-plugin`，`io.caliper.apm.symbols`）、iOS Fastlane 插件（`packages/fastlane-plugin`）与复合 GitHub Action（`packages/github-action`）。

## 深度诊断、OTLP 异步推送与 AI 原生 MCP Server（v1.0 – v1.10）

详见 [`docs/roadmap-v1.0.md`](docs/roadmap-v1.0.md)：
- **CPU 火焰图与裁剪堆内存诊断 *（服务端引擎就绪 + Android Java 采样器）***：服务端折叠栈自动符号化火焰图（`GET /v1/diagnostics/profiles/{id}/flamegraph`）与跨版本类级 Dominator Diff（`POST /v1/diagnostics/heap/compare`）已全链路验证；Android 端提供 `CaliperCpuProfiler`，iOS/Hermes 自动采样器与端侧可选内存诊断插件已排入 v2.0 路线图。
- **强审计远程日志捞取任务**：支持按 `user_hash` / `install_hash` 下发带 TTL 与字节上限的远程日志拉取任务，SDK 轮询 + Gzip 摘要回传，全链路 Fail-Closed 写入审计日志。
- **全双工 OTLP 互通与 Trace 异步推送**：原生接收标准 OpenTelemetry Traces / Logs（`POST /v1/otlp/v1/traces`、`/v1/otlp/v1/logs`），支持聚合指标拉取（`GET /v1/otlp/export`）与 W3C `CLIENT` Span 查询（`GET /v1/otlp/export/traces`），并内置有界异步 **OTLP Trace Push Exporter**（`--otlp-traces-endpoint=http://tempo:4318/v1/traces`），直接向 Jaeger / Grafana Tempo / OTel Collector 推送移动端全链路 Span。
- **AI 原生 Caliper MCP Server（`POST /v1/mcp` + `caliper-mcp`）**：纯 Go 零依赖实现标准 Model Context Protocol (MCP) JSON-RPC 2.0（兼容 Streamable HTTP `2025-06-18` / `2025-03-26` / `2024-11-05`、`Origin` 防 DNS Rebinding 校验、Fail-Closed RBAC `app_id` 租户隔离与输出体积保护），向 Cursor / Claude Code / Codex / Antigravity 暴露 `caliper_overview`、`caliper_compare_releases`、`caliper_evaluate_gate`、`caliper_scan_regressions`、`caliper_list_issues`、`caliper_inspect_issue`（自动触发 R8/dSYM/SourceMap/Swift 5+ 符号化）六大诊断工具，且**服务端无需配置外部 LLM Key**：
  ```json
  {
    "mcpServers": {
      "caliper-apm": {
        "command": "caliper-mcp",
        "args": ["--server", "http://127.0.0.1:8080"]
      }
    }
  }
  ```
- **Zero-Lie 版本包体积追踪（`binary_size_bytes` + `binary_artifact_kind`）**：由 CI 插件（`caliper-symbols` / Fastlane）在注册版本时可选上报 `apk`/`aab`/`ipa`/`xcarchive`/`bundle` 字节数，端侧开销为 0；门禁报告仅对比同类型产物（绝不拿 `.aab` 与 `.apk` 混比，缺失绝不补零）。

## 生态差异化定位（零夸大诚实对标）

| 开源/商业方案 | 协议 | 核心长板 | 与 Caliper APM 的定位差异 |
|---|---|---|---|
| **腾讯 Matrix / 字节开源工具（`btrace`/`ShadowHook`/`ByteHook`/`Tailor`）/ 快手 KOOM / xCrash** | BSD-3 / Apache-2.0 / MIT | 业界顶尖的端侧底层 Hook、HPROF 裁剪、Perfetto 深度 Trace 与内存泄漏分析。 | 属于纯端侧诊断工具，缺少开箱即用的采集后端、符号化流水线、大盘与发版门禁。Caliper 提供 15 分钟自托管的端云全栈平台，核心 SDK 坚守零 Hook 低开销安全红线。 |
| **Sentry Self-Hosted** *（及轻量化平替 GlitchTip / Bugsink）* | FSL-1.1-Apache-2.0 *（允许企业免费内部自托管，禁止包装为竞品 SaaS，2 年后转 Apache-2.0）* | 30+ 语言成熟生态、移动端 Session Replay 与海量集成插件。 | Sentry 自托管需 16~32GB 内存与 20+ 容器；GlitchTip/Bugsink 侧重错误追踪而非移动端性能回归体系。Caliper 为纯 OSI **Apache-2.0**，单二进制零 CGO 启动（内置有界去重与 WAL 快照压缩），生产可平滑切 ClickHouse + PG + S3，聚焦移动端 Zero-Lie 统计学发版门禁。 |
| **Measure.sh** | Apache-2.0 | 新一代移动端全栈开源 APM，支持 Android/iOS/Flutter/RN(Expo)/KMP、轻量视图树快照时间线、Adaptive Capture 与 MCP Server。 | Caliper 差异化在于 **Zero-Lie 统计学严谨度**（单机与 ClickHouse 全链路 DDSketch ±2%、零帧与算法隔离、窗口聚类 Z 检验、BH-FDR 多重检验校正）、零 CGO 单二进制极速启动 + 纯 Go Swift 5+/DWARF 符号化，以及原生 CI/CD `caliper-gate` 阻断门禁。 |
| **OpenTelemetry Mobile / Embrace 开源 SDK + Grafana / SigNoz** | Apache-2.0 | 开放中立的 OTLP 生态，易于与后端微服务可观测性统一。 | 需自建移动端流畅度/FOOM 口径、原生 dSYM/R8/SourceMap 自动符号化与灰度统计门禁。Caliper 通过 OTLP 接收 + 异步 Trace 推送与其无缝互通，同时开箱即用补齐移动端专属分析闭环。 |

## 企业级控制台 2.4、生产 SDK、DDSketch 与 CI 质量门禁（v1.1 – v1.10）

详见 [`CHANGELOG.md`](CHANGELOG.md)：
- **纯 Go 零依赖 DDSketch（$\pm 2\%$）长尾分位与 `DDSketchKSTest`（`v1.10`）**：洁净室实现可合并对数分位草图（`server/internal/contract/ddsketch.go`），在启动、资源峰值、网络传输、自定义链路、14 天趋势、告警规则、OTLP 导出与 Console 2.4 全链路提供误差 $\le 2\%$ 的 `P50 / P90 / P95 / P99` 指标与桶内 `ddsketch_ks` 回归检测，并无缝回退兼容 19 桶直方图线性插值。
- **纯 Go Swift 5+ Demangler、全链路 W3C `traceparent` 与 AGP 8.7 + R8 全混淆实测（`v1.9`）**：零 CGO Swift 5+ 符号还原（覆盖常见生产栈帧，经 32 组单元向量 + 18 个实编符号与 Apple `xcrun swift-demangle` 交叉验证匹配，未知语法诚实回退）；Android（`CaliperOkHttpNetworkBridge`）、iOS（`CaliperAutoURLSessionDelegate`）、RN（`wrapFetchForDenominator`）三端支持域名白名单注入 W3C `traceparent`，贯通 ClickHouse、Console 2.4 `trace:` 标签与 `GET /v1/otlp/export/traces`；通过 `examples/android-agp-r8-sample` 完成真实 AGP 8.7.3 + OkHttp 4.12 + NDK 27 + R8 8.7.18 `dexdump` 实测。
- **Console 2.4 企业级多维可视化**：中英双语、**暗黑 / 亮色一键主题切换**、**全局机型 / 系统版本 / 场景 / 版本 / 适配层筛选栏（`GET /v1/ops/dimensions`）**、**可分享 URL 筛选状态**、**一键导出 JSON 诊断报告**、Vite vendor 拆包（首屏业务包约 `33 KB` gz）、14 天版本放量与 P50/P90/P95/P99 趋势标注、19 桶分布直方图、5 域网络失败占比、HTTP 状态码分布、**慢请求/异常请求单次瀑布与 W3C `trace:` 抽屉**、**核心业务链路自定义多阶段追踪（`GET /v1/analytics/journeys`）**、跨页下钻与可视化单用户会话时间线。
- **CI/CD 灰度质量门禁（`caliper-gate` CLI + GitHub PR Comment Action）**：基于 Z 检验 + KS 检验 + 符号覆盖率的零谎言发版红绿灯（`POST /v1/analytics/release-gate`）及一键 PR 评论 Action（`packages/github-action/release-gate/action.yml`），退出码 `0`（PASS）、`2`（BLOCK 显著劣化）、`3`（GATED 样本不足）：
  ```bash
  caliper-gate --server http://127.0.0.1:8080 --platform android --baseline 1.0.0 --candidate 1.1.0 --format markdown
  ```
- **稳定性异常流转与影响分布**：异常全生命周期处置（待处理 / 处理中 / 已修复 / 已忽略、负责人、修复版本、备注 + 审计留痕）、按机型/系统诚实计数筛选、单异常的机型/系统/版本 ECharts 分布图，以及抽屉内一键跳转单点追查。
- **远程动态采样策略与运维自检**：控制台热更新采样率（% / PPM）、单设备 24h 上限、租约 TTL 与各探针开关（`GET/PUT /v1/policy`），配套告警规则一键测通/启停/删除、数据源健康自检（`GET /v1/ops/source-health`）与 Prometheus `GET /metrics`。
- **生产端无侵入自动接入、底层深度与无状态集群高可用（`v1.6 – v1.8`）**：**Android Gradle ASM9 字节码插桩插件（`io.caliper.apm.autoinstrument`，自动织入 OkHttp 与 `@CaliperTrace`）**、**iOS 一行集成 `CaliperIOSAgent` + SwiftUI `.caliperScene()` + 安全透传 `URLSession` 代理**、**双端电量/热节流、零 PII 磁盘审计与 WebView/WKWebView 白屏/首屏探针**、**iOS 实时 C/Mach 崩溃内核（`8.4 KiB`）与 9 分支 FOOM 状态机**、**Android NDK C++ 信号崩溃 & `SIGQUIT` ANR 库（`libcaliper_native.so`，零 `libc++_shared.so` 依赖）与主线程 `Looper` 耗时回溯**、**运行时零 CGO PostgreSQL `MetaStore` 驱动（`--meta-driver=postgres`）**、AWS SigV4 S3/MinIO、ClickHouse、原生 `.dSYM`/`.so` DWARF 符号化与 Kubernetes Helm Chart。

---

## 文档中心

| 文档 | 内容概览 |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | 端到端系统拓扑（Mermaid）、Zero-Lie 直方图与统计门禁原理、Clean-Room 架构设计 |
| [`docs/self-hosting.md`](docs/self-hosting.md) | Embedded WAL 单二进制 vs ClickHouse + S3 高可用集群、Helm 部署、RBAC、PR 门禁与 Prometheus |
| [`docs/sdk-android.md`](docs/sdk-android.md) | 纯 Kotlin Android 接入（`CaliperAndroidAgent`、`FrameMetrics`、Watchdog、Gradle R8 插件、`vivo V2343A` 实测） |
| [`docs/sdk-ios.md`](docs/sdk-ios.md) | 纯 Swift SPM iOS 接入（`CaliperClient`、`CaliperMetricKitBridge`、Watchdog、Fastlane dSYM 插件、`iPhone 13 Pro Max` 实测） |
| [`docs/sdk-react-native.md`](docs/sdk-react-native.md) | React Native 一等适配层（`wrapFetchForDenominator`、550× 过桥流量压缩、Hermes SourceMap v3、原生桥接） |
| [`spec/metrics-contract.md`](spec/metrics-contract.md) & [`NORTH_STAR.md`](NORTH_STAR.md) | 冻结北极星宪章与 Zero-Lie 统计契约 |

---

## 开源协议

基于 [Apache License 2.0](LICENSE) 开源。
