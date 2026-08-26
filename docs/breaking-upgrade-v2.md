# roost-skill v2 破坏性升级手册

本手册是 v1 升级到 v2 的唯一生产迁移路径。v2 不保留双读、兼容别名或静默降级；旧数据
被拒绝时必须先排空或显式转换，不能删除校验绕过启动失败。

## 1. 版本与依赖

v2 的 Go 模块路径已改为：

```go
github.com/tjbdwanghaibo/roost-skill
```

所有 import 必须带 `/v2`。发布和部署基线为：

- `cube-core v1.6.2`；
- `cube-kit v1.6.1`；
- `roost-skill v2.0.0`。

禁止在生产 `go.mod` 中使用本地 `replace`。仓库内的 replace 只允许存在于
`integration/sync-e2e` 测试模块。

## 2. 不兼容清单

| 范围 | v2 行为 | 迁移动作 |
| --- | --- | --- |
| Go module | 模块路径增加 `/v2` | 更新所有 import、mock、工具与代码生成模板 |
| Runtime checkpoint | 只接受 checkpoint v2，严格校验大小、记录数、checksum、Program、Host authority 和 world revision | v1 停服后排空，不把 v1 checkpoint 交给 v2 |
| FileOutbox | 只接受版本化 checksum envelope | v1 排空到 pending=0；旧目录只归档，不与 v2 共用 |
| skillcompose | 只接受 `skillcompose/v2` 合同；候选必须携带精确 authority、source identity 和 feature origin/transform | 重新 BuildContract、重新生成并校验候选 |
| Runtime trace | 删除 `RuntimeOptions.TraceLimit` | 使用 `TraceLimits.MaxBuffer` |
| Process Host | 删除 `ProcessCommandPayload`、`ProjectileStepCommand` 和 `ProcessStepCommand.Payload` | Host 只处理非 nil 的类型化 `MotionStep` |
| Status result | 删除 `StatusResult.Stacks` | 读取 `CurrentStacks`，差量使用 `PreviousStacks/RemovedStacks` |
| External event | 外部事件必须提供稳定且非零的 EventID | 在接入层分配可重放的幂等 ID |

Schema/catalog 的业务演进仍可采用客户端双版本窗口，但这不代表 checkpoint、outbox 或
composition contract 可以双读。持久化格式迁移和网络 schema 迁移是两件独立的事。

## 3. 停服前排空

每个 shard/room 按以下顺序执行，不允许新旧进程同时写同一 History、Outbox 或世界存储：

1. 从服务发现摘除实例，停止接收新 cast、外部事件和新 observer。
2. 等待已进入 Runtime 的 tick/transaction 完成。
3. 对所有 observer/key 调用 `Coordinator.Flush`。
4. 循环调用 `RetryPending`，等待客户端 ACK；确认 `Coordinator.Health().Outbox.Pending == 0`。
5. 调用 `CloseObserver`。该操作先删除 durable outbox，再删除 History；正常 ACK 会先在
   observer/key 生命周期锁内校验，History WAL 失败时立即从 History 重建 outbox。任何一步失败都停止
   关服流程并报警。
6. 持久化相互匹配的 world/runtime 最终状态，执行干净停机。
7. 将 v1 checkpoint、History 和 outbox 目录作为一个只读集合归档。不要只备份其中一项。

无法等待所有客户端 ACK 时，必须把玩家移出旧 session，并在 v2 首次接入时发新的 full/reset；
不能把未 ACK 的 v1 outbox 文件复制到 v2。

## 4. v2 首次启动

1. 使用全新的 v2 outbox 目录和经过验证的 v2 History/WAL 目录。
2. 先恢复权威世界，再以匹配的 `WorldRevision`、`AuthorityIdentity` 和 `ProgramResolver`
   创建或恢复 Runtime。
3. 创建 Coordinator 时启用 durable outbox，并显式配置全部容量上限。
4. 在接受流量前执行一次 `ReconcilePending`；正常重试只调用 `RetryPending`，不要每个 tick
   全量扫描 History。
5. 为每个新 session 先发布 manifest、state full、presentation reset/snapshot，客户端确认后
   才允许 delta。
6. 检查 History、Coordinator/Outbox、publisher/subscriber 指标与告警，再加入服务发现。

推荐基线（容量必须由压测结果替换）：

```go
store, err := skillsync.NewFileOutboxStoreWithOptions(outboxDir, skillsync.FileOutboxOptions{
    MaxRecords:     100_000,
    MaxRecordBytes: 2 << 20,
})
if err != nil { return err }

outbox, err := skillsync.NewOutbox(skillsync.OutboxOptions{
    Store:               store,
    RequireDurable:      true,
    MaxPendingPackets:   100_000,
    MaxPendingBytes:     256 << 20,
    MaxPendingPerStream: 4_096,
    MaxPendingAge:       24 * time.Hour,
    MaxPublishBatch:     512,
})
if err != nil { return err }
```

业务 Host 只有在 Runtime 是事件流唯一消费者时才实现 `HostEventCompactor`。参考 Host 可用
`NewMemoryHostWithOptions(authority, MemoryHostOptions{CompactEvents: true})`；共享事件消费者场景
必须关闭压缩，避免提前丢弃其他消费者尚未读取的事件。

## 5. compose 候选迁移

生成器只能使用 `DeriveContractPromptView` 返回的 digest-covered grant。候选的每个 feature
必须声明来源和变换；当前构建器授予的是 `identity`：

```go
candidate.FeatureOrigins = []skillcompose.FeatureOrigin{
    {
        Feature:   "effect.damage",
        SourceID:  sourceProfile.SkillID,
        Transform: skillcompose.TransformIdentity,
    },
}
```

缺失、重复、额外来源，来源与 feature 不匹配，或使用合同未授权的 transform，都会被
`ValidateCandidate` 拒绝。不要从未签名的 profile 列表生成 prompt；使用
`DeriveContractPromptView(contract)`。

## 6. 回滚边界

- v2 尚未接收流量、未写入世界/History/outbox 时，可以整体切回已归档的 v1 数据集。
- v2 一旦接收写流量，禁止把单个 v2 文件复制回 v1，也禁止仅回滚二进制。应前向修复，或
  停服后整体恢复同一时间点的 v1 world + runtime + History + outbox 备份。
- 回滚必须更换 session/epoch 并向客户端发送 full/reset，防止旧 ACK 确认新流量。

## 7. 发布验收

除 [生产门槛](production-readiness.md) 外，升级演练必须覆盖：排空、强杀进程、WAL/outbox
恢复、重复 ACK、并发 `RetryPending`、损坏/旧格式启动失败、首次 full/reset、回滚前置条件。
灰度至少按独立 shard 进行，任何 pending age、publish failure、history gap、checkpoint reject
异常增长都停止扩容。
