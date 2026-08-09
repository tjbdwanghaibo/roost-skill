# Skill v2 当前实现学习手册

本文对应当前 `codex/skill-v2-extract` 分支中的实现。目标是帮助新读者从一份
JSON 技能定义，顺着真实调用链走到确定性的世界交互，而不是先被大量
`wire_*`、`ir_*`、`program_*` 文件淹没。

## 1. 先建立全局心智模型

Skill v2 是一个受限的、可验证的技能 DSL。它不直接执行 JSON；每次执行都必须
经过四层边界：

```text
Skill JSON
  -> Parse（严格 Wire 定义）
  -> Compile（IR、静态证明、诊断）
  -> Lower（不可变 Program）
  -> Runtime（确定性调度）
  -> Host（唯一允许读写世界的接口）
```

外部系统不应绕过这些边界：

| 需求 | 正确入口 | 不应做的事 |
| --- | --- | --- |
| 接收技能 JSON | `Parse` | 直接反序列化到运行时 Program |
| 构建可执行技能 | `Compile` | 在 Runtime 内解释 JSON 或字符串键 |
| 执行主动技能 | `Runtime.Activate` | 直接调用 Host 写世界 |
| 执行被动技能 | `Runtime.ActivatePassive` | 把被动技能当主动技能施放 |
| 给 UI/组合系统提供信息 | `Inspect*` | 访问 Program 私有执行字段 |
| 修改游戏世界 | `Host` 的查询/命令接口 | 在编译器或 Inspector 修改世界 |

这套分层同时承担三个职责：解析边界保证输入封闭；编译器保证定义可执行且有界；
Runtime 只消费已证明的 Program，并通过 Host 保持世界权威性。

## 2. 推荐阅读顺序（约 3～5 小时）

### 第一轮：一次最小施放（30 分钟）

从 [simple_damage.json](../skillv2/testdata/simple_damage.json) 开始，只做一件事：
输入目标，对目标造成物理伤害，然后结束。

按以下顺序跳转：

1. [parse.go](../skillv2/parse.go) 的 `Parse`：确认 schema、严格对象解码与顶层定义。
2. [lower.go](../skillv2/lower.go) 的 `Compile`：它是公开的编译入口。
3. [runtime.go](../skillv2/runtime.go) 的 `NewRuntime`、`Activate` 和 `startLocked`：理解 Cast 的创建。
4. [executor.go](../skillv2/executor.go)：查看一个 Program operation 如何被执行。
5. [memory_host_effect.go](../skillv2/memory_host_effect.go)：以 `MemoryHost` 为例观察伤害如何真正提交。
6. [acceptance_test.go](../skillv2/acceptance_test.go)：该测试把 fixture 走完
   Parse → Compile → Inspect → Activate → Advance → 终态检查。

完成这一轮后，应能回答：伤害量从 JSON 中何时成为强类型值？为什么 Runtime 不需要
再解析 `"physical"`？为什么世界写入最终只发生在 Host？

### 第二轮：编译器如何拒绝不安全定义（60 分钟）

阅读 [compile.go](../skillv2/compile.go)。`compileToArtifactsInternal`
列出的 Pass 顺序就是当前编译语义的主目录：

1. `normalize`：Wire Definition 转为封闭 IR，并记录源路径。
2. `shape`：校验 Flow、Effect、Select、Process 的基本结构。
3. `authority_capability`：把属性、资源、状态、模板、标签等字符串解析到
   `CompileEnvironment` 提供的权威 Handle，并验证能力目录。
4. `gameplay_tags`、`input_state`、`temporal`：验证标签类别、施放输入、
   Persistent/Shared State 与时间快照。
5. `type_snapshot`、`optional_quantity`、`effect_result_scope`：检查值类型、
   单位/快照点、Effect Result 的作用域与字段。
6. `graph`、`memory`、`lifetime_ownership`：证明 phase/flow 可达、Memory 已初始化、
   生命周期和拥有关系有界。
7. `motion`、`event_proc`、`identity_random`、`budget`：限制过程运动、被动递归、
   身份/随机位点以及最坏执行预算。
8. `visual`：验证表现资产只引用目录中允许的类别与主题。
9. `lower`：仅标记全部静态证明已完成；真正的 Program lowering 在公开 `Compile` 中发生。

不要把这些 Pass 当作可以随意交换的 lint 集合。后续 Pass 假定前置 Pass 已将名称、
类型和作用域收窄，例如 `motion` 依赖已解析的能力和类型信息，`budget` 依赖图与过程
调用边界。

建议配合阅读：

- `compile_*_test.go`：静态拒绝用例；
- [compile_environment.go](../skillv2/compile_environment.go)：默认目录和限制；
- [canonical_definition.go](../skillv2/canonical_definition.go)：源定义的稳定摘要；
- [diagnostic.go](../skillv2/diagnostic.go)：诊断代码和稳定排序约定。

练习：把 `simple_damage.json` 的 `damage_type` 改成不存在的键，再运行对应测试或
验收测试，观察诊断产生在 authority/capability 边界，而不是在运行时。

### 第三轮：从 IR 到 Program（40 分钟）

Wire 层与 IR 层回答“用户写了什么”；Program 层回答“Runtime 可以只按索引执行什么”。

阅读顺序如下：

1. `wire_*.go`：JSON 允许的封闭语法。尤其是 `wire_definition.go`、
   `wire_flow.go`、`wire_effect.go`、`wire_input.go`。
2. `ir*.go` 和 `compile_normalize.go`：标准化后的强类型中间表示。
3. [lower.go](../skillv2/lower.go)：把名称解析成 Handle、MemoryIndex、
   LocalIndex、OperationIndex，并收集 snapshots、random sites、event plans。
4. `program_*.go`：Program 内部的执行指令和索引布局。
5. [inspect.go](../skillv2/inspect.go)：唯一推荐给外部消费者的 Program
   观察面。

关键不变量：Program 是编译结果，Runtime 不应重新解析 DSL 字段或回头查询源 JSON。
所有可变状态都属于 cast、scheduler 或 Host；Program 自身是跨施放共享的只读计划。

## 3. Runtime：把 Program 变成确定性行为

Runtime 的核心类型位于 [runtime.go](../skillv2/runtime.go)：

- `RuntimeOptions`：匹配种子、任务/trace 限制等运行时配置；
- `CastInput`：主动施放输入的统一载体；
- `castInstance`：每次施放自己的 phase、memory、可见 world revision、状态；
- `Runtime`：casts、cooldown、scheduler、被动事件账本和 trace 缓冲。

一次主动施放的时间线如下：

```text
Activate
  -> Start / startLocked
  -> 输入标准化与 Cast Window 准备
  -> 进入当前 phase 的 enter flow
  -> executor 执行 operation，必要时向 scheduler 注册后续任务
  -> Advance(tick) 以稳定顺序消费到期任务
  -> Finish / Cancel / Release，清理 process 和 cast 状态
```

重点文件：

- `runtime_input.go`：位置、目标、双点、拖拽、路径输入的运行时校验与归一化；
- [runtime_cast_window.go](../skillv2/runtime_cast_window.go)：windup、
  commit、recovery、`Cancel` 和 `Release`；
- [scheduler.go](../skillv2/scheduler.go)：`Advance`、稳定排序和任务执行；
- `runtime_dispatch.go`、`runtime_event.go`：phase 事件与 process 信号如何进入 flow；
- [runtime_proc.go](../skillv2/runtime_proc.go)：`ActivatePassive` 与
  递归/同根事件保护；
- `runtime_state.go`、`runtime_ability.go`、`runtime_temporal.go`：状态、能力控制、
  快照等专用操作的运行时桥接。

### 一个重要区分：主动与被动

主动技能从 `Activate(program, CastInput)` 开始。被动技能没有用户施放输入，应由事件
驱动 `ActivatePassive(program, EventContext)` 入队，再由 `Advance` 执行。

当前 fixture 验收已经显式区分二者：被动 fixture 使用 `ActivatePassive` 并给出事件根、
owner、source、target（以及需要时的 `Result: "kill"`），主动 fixture 才检查 Cast 的
终态。这是新增被动 fixture 时最容易遗漏的一点。

## 4. Host：世界权威边界

`Host` 不是测试替身专用接口，而是 Runtime 与游戏世界之间的正式边界。Runtime 通过它：

- 读取属性、位置和实体信息；
- 执行选择查询；
- 支付成本；
- 提交伤害、治疗、状态、生成、移动等 Effect；
- 推进/停止 Process；
- 读写持久或共享 State；
- 读取事件流和世界 revision。

建议先读 [host.go](../skillv2/host.go)，再读 `host_*.go` 中的命令和
结果类型，最后读 `memory_host*.go`。`MemoryHost` 是可重复的参考实现和测试世界，
不是生产服务器的替代品。

World revision 是关键防线：Runtime 的 query/command 会携带期望 revision，Host 负责
拒绝已失效读取或提交。因而不要缓存 Host 返回的可变对象，再在后续 tick 假设其仍然有效。

## 5. 过程与高级能力的阅读地图

以下表按功能给出最小切入 fixture 与主要实现位置：

| 能力 | 先看 fixture | 主要代码 |
| --- | --- | --- |
| Cast policy/window | `toggle_aura`、`hold_beam`、`charge_projectile`、`ammo_burst`、`cast_window_interrupt` | `runtime_cast_policy.go`、`runtime_cast_window.go` |
| 输入约束 | `path_projectile`、`two_point_wall`、`portal_pair` | `wire_input.go`、`compile_input.go`、`runtime_input.go` |
| 运动/过程 | `carry_dash`、`tracking_boomerang`、`beam`、`projectile_area` | `process*.go`、`process_motion.go`、`compile_motion.go` |
| Area 成员事件 | `area_heal`、`area_membership`、`entity_scoped_aura` | `process_area.go`、`area_test.go` |
| 数值与快照 | `dynamic_numeric`、`attribute_scaling_snapshot` | `compile_quantity.go`、`compile_snapshot.go`、`runtime_eval.go` |
| 状态 | `status_modifier`、`status_cleanse`、`status_steal` | `compile_status.go`、`runtime_select.go`、`memory_host_status.go` |
| State/能力控制 | `persistent_mark`、`shared_state_combo`、`cooldown_refund`、`ability_disable` | `compile_state.go`、`runtime_state.go`、`runtime_ability.go` |
| Owned Entity | `owned_trap`、`owned_pet_command` | `compile_owned_entity.go`、`runtime_owned_process.go`、`memory_host_owned_entity.go` |
| Temporal/Result | `temporal_rewind`、`effect_result_kill_branch` | `compile_temporal.go`、`runtime_temporal.go`、`runtime_effect_result.go` |
| Passive proc | `passive_counter`、`passive_proc_guard`、`ammo_on_kill` | `compile_proc.go`、`runtime_proc.go` |

所有 37 个 fixture 位于 [testdata](../skillv2/testdata)。
`acceptance_test.go` 使用目录发现机制：新增 JSON 若没有明确的输入、推进 tick、release
或 passive 配置，测试会失败。因此 fixture 既是可运行示例，也是变更清单。

## 6. Inspector 与 skillcompose：只读的上层消费

不要让 UI、提示词生成或技能组合层了解 Runtime 私有 operation。它们应通过
`Inspect`、`InspectMetrics`、`InspectInputLayout`、`InspectSelections`、
`InspectEffectResults` 等只读视图消费 Program。

[skillcompose](../skillcompose) 展示了这一原则：

1. `profile_extract.go` 从 Inspector 提取 `SkillProfile`；
2. `contract_builder.go` 将多个 profile 和 caller policy 收紧为 composition contract；
3. `validator.go` 验证候选 profile 不会超出已授予的功能、预算和因果连通性；
4. `prompt_view.go` 只派生提示词需要的稳定投影。

阅读 `skillcompose` 时，若发现需要访问 Program 未公开字段，应优先新增一个稳定的
Inspector 视图，而不是打破包边界。

## 7. 可观测性、回放和提示词契约

### Trace

[trace.go](../skillv2/trace.go) 维护有界、被动的 `TraceEvent` 缓冲。
执行路径只记录事件；调用方在安全的外部时机调用 `FlushTrace`，才把缓冲发送给
`TraceSink`。sink 失败不会反向影响游戏逻辑，也不会丢弃尚未成功发送的事件。

### Record / Replay

[replay.go](../skillv2/replay.go) 提供测试与排障适配器：

- `RecordingHost` 保存 Host 调用的顺序、请求调试键、类型化结果、错误和 revision；
- `ReplayHost` 仅在下一次调用的种类与请求一致时返回已录制结果；
- `AssertComplete` 用于发现未消费记录。

它适合确定性测试，不是生产世界模型。回放不匹配会触发 `ErrReplayMismatch`（接口中
不能返回错误的方法会 panic），因此测试中应在外层捕获并报告上下文。

### Prompt

系统提示词位于 [ai-skill-v2-system-prompt.md](ai-skill-v2-system-prompt.md)。
其中 fenced JSON 示例由 `prompt_test.go` 解析并编译。修改 prompt 中的 DSL 示例时，
必须一并运行该测试，避免文档说法与实际 wire contract 漂移。

## 8. 新功能的正确开发顺序

以新增一种 Effect 为例，按以下顺序实现，通常最少返工：

1. 明确能力目录和 Host 命令/结果；先确认它是世界写入、查询还是纯 Program 元数据。
2. 在 Wire 层加入严格解码，同时拒绝未知字段。
3. 在 IR 与 normalize 中显式建模，不使用 `map[string]any` 逃逸类型检查。
4. 在合适的 compile pass 中验证 capability、类型、作用域、生命周期和预算。
5. 在 lowering 中把名称降为 Handle、索引或具体 Program operation。
6. 在 executor/runtime 中调用 Host，并处理成功、预期失败和 Host 合约失败。
7. 通过 Inspector 公开确实需要被上层读取的稳定事实。
8. 添加：解析拒绝测试、编译诊断测试、Runtime 成功/失败测试、一个具有独立行为的 fixture。
9. 若提示词包含该能力，添加或更新可编译 JSON 示例。

不要只添加 Wire JSON 分支：那会使 DSL “能解析但不能证明/执行”。也不要只给
`MemoryHost` 加行为：那会制造一个没有语言入口的世界命令。

## 9. 验证流程

在 PowerShell 中（缓存目录可按本机情况调整）执行：

```powershell
$env:GOCACHE = 'D:\whb_s\cube\.tmp-skillv2-gocache'

# 先验证所有 fixture 的 Parse -> Compile -> Inspect -> Run 路径。
go test ./skillv2 -run TestAllFixturesParseCompileInspectAndRun -count=1

# 再做包级回归和静态检查。
go test ./skillv2 ./skillcompose -count=1
go vet ./skillv2 ./skillcompose

# 改动 Runtime、Scheduler、Trace 或 Host 并发边界时必须执行。
go test -race ./skillv2 ./skillcompose -count=1

# 修改 parser 或 Wire decode 时执行短时 fuzz。
go test -run=^$ -fuzz=FuzzParseGeneratedNeverPanics -fuzztime=10s ./skillv2

# 扩大到 gameplay 子树，并检查补丁空白错误。
go test ./game/gameplay/... -count=1
git diff --check
```

测试失败时，先按层定位：`Parse` 失败看 Wire；有 Diagnostic 看对应 pass；Program
inspection 不符看 lowering；cast 状态不符看 Runtime/Scheduler；世界结果不符看
Host command/result 和 revision。这样通常不需要在整条链路中盲目断点。

## 10. Visual 与 Sync 的继续阅读路线

完成 Runtime 主链后，按以下顺序继续：

1. `wire_visual.go`、`compile_visual.go`、`program_visual.go`：Visual 从严格 wire 到
   canonical manifest；
2. `presentation.go`、`presentation_assets.go`：静态 mount、动态生命周期事件和客户端
   资源解析边界；
3. `runtime_sync.go`、`runtime_value_json.go`：权威全量状态、强类型 delta 和游标过期；
4. `skillsync/skillsync.go`：三类业务 record 如何进入通用 Packet；
5. `skillsync/coordinator.go`：observer 策略、source cursor、History 和自动恢复；
6. `skillsync/applier.go`：客户端 schema/sequence/manifest 校验与幂等应用；
7. `cube-core/syncstream`：网络序号、ACK、replay、full fallback、持久化与指标；
8. `cube-kit/syncstream`：NATS/JetStream envelope、observer 隔离和背压。

完整生产接入、发布与故障注入流程见
[Visual 与数据同步生产指南](visual-sync-production-guide.md)。
