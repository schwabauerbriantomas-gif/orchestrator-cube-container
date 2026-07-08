# network-agent

`network-agent` 是开源版 MVP 新增的节点本地网络组件。

它不来自闭源仓的整目录复制，而是在保持 `Cubelet`、`mvs` 同源迁移的前提下新增。

当前职责边界：

- 承接从 `Cubelet` 迁出的节点本地网络编排
- 复用 `mvs/cubevs` 的本地网络执行能力
- 对 `Cubelet` 提供独立的 `ensure / release / reconcile / health` 接口

当前目录已补上单机 MVP 所需的最小执行闭环：

- `EnsureNetwork` / `ReleaseNetwork` / `ReconcileNetwork` / `GetNetwork`
- 本地 TAP 创建与 `cubevs` 映射
- HostPort 到 guest 服务的用户态代理
- 本地状态落盘与启动恢复

仍然不在首发范围内的内容：

- VPC / ENI / SubENI
- `networkd`
- `CubeGW` / tunnel group 编排

当前目录仍保留按模块继续收敛的空间：

- `cmd/`
- `api/v1/`
- `internal/`

## 当前可用内容

- `api/v1/network_agent.proto`：最小 RPC contract 草案
- `cmd/network-agent/main.go`：可执行入口占位
- `internal/service/service.go`：最小 service 抽象和 noop 实现
- `internal/httpserver/server.go`：`/healthz`、`/readyz` 探针服务
