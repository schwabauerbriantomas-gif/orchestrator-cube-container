# Network Agent 开发指南

本文档介绍 Network Agent 的开发、构建和测试流程。

---

## 开发环境要求

### 必需工具

| 工具 | 版本 | 用途 |
|------|------|------|
| Go | 1.24.0+ | 编译和运行 |
| protoc | 3.x | Protocol Buffers 编译 |
| protoc-gen-go | latest | Go 代码生成 |
| protoc-gen-go-grpc | latest | gRPC 代码生成 |

### 安装依赖

```bash
# 安装 Go (如未安装)
# 参考: https://golang.org/doc/install

# 安装 protoc 插件
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

### 系统依赖

运行 Network Agent 需要 Linux 环境，并具备:
- root 权限 (创建 TAP 设备)
- netlink 支持
- eBPF 支持 (内核 5.x+)

---

## 项目结构

```
network-agent/
├── api/v1/                    # API 定义
│   ├── network_agent.proto    # Protobuf 定义
│   ├── network_agent.pb.go    # 生成的 Go 代码
│   └── network_agent_grpc.pb.go
├── cmd/
│   └── network-agent/
│       └── main.go            # 程序入口
├── internal/
│   ├── fdserver/              # TAP FD 服务器
│   ├── grpcserver/            # gRPC 服务器
│   ├── httpserver/            # HTTP 服务器
│   └── service/               # 核心业务逻辑
├── docs/                      # 文档
├── go.mod
├── go.sum
└── README.md
```

---

## 构建

### 构建二进制

```bash
# 进入项目目录
cd /data/netdev/cube-sandbox/network-agent

# 构建
go build -o bin/network-agent ./cmd/network-agent

# 或使用 go install
go install ./cmd/network-agent
```

### 交叉编译

```bash
# Linux AMD64
GOOS=linux GOARCH=amd64 go build -o bin/network-agent-linux-amd64 ./cmd/network-agent

# Linux ARM64
GOOS=linux GOARCH=arm64 go build -o bin/network-agent-linux-arm64 ./cmd/network-agent
```

### 构建标记

```bash
# 启用 race 检测 (开发/测试用)
go build -race -o bin/network-agent ./cmd/network-agent

# 生产构建 (去除调试信息)
go build -ldflags="-s -w" -o bin/network-agent ./cmd/network-agent
```

---

## 代码生成

### Protocol Buffers

当修改 `api/v1/network_agent.proto` 后，需要重新生成代码:

```bash
cd /data/netdev/cube-sandbox/network-agent

make proto
```

生成的文件:
- `api/v1/network_agent.pb.go` - 消息类型
- `api/v1/network_agent_grpc.pb.go` - gRPC 服务
- `../Cubelet/pkg/networkagentclient/pb/network_agent.proto` - 同步后的 Cubelet 客户端 proto
- `../Cubelet/pkg/networkagentclient/pb/network_agent.pb.go` - Cubelet 客户端消息类型
- `../Cubelet/pkg/networkagentclient/pb/network_agent_grpc.pb.go` - Cubelet 客户端 gRPC 代码

---

## 测试

### 运行所有测试

```bash
go test ./...
```

### 运行特定包的测试

```bash
# 测试 service 包
go test ./internal/service/...

# 测试 HTTP server
go test ./internal/httpserver/...

# 测试 gRPC server
go test ./internal/grpcserver/...
```

### 测试覆盖率

```bash
# 生成覆盖率报告
go test -coverprofile=coverage.out ./...

# 查看覆盖率
go tool cover -func=coverage.out

# HTML 报告
go tool cover -html=coverage.out -o coverage.html
```

### 测试特定函数

```bash
# 运行匹配的测试
go test -run TestEnsureNetwork ./internal/service/...
```

### 并行测试

```bash
# 指定并行度
go test -parallel 4 ./...
```

### Verbose 输出

```bash
go test -v ./...
```

---

## 测试设计

### 单元测试模式

项目使用**函数变量替换**模式进行 mock:

```go
// production code
var newTapFunc = func(...) (*tapDevice, error) {
    // real implementation
}

// test code
func TestSomething(t *testing.T) {
    // save original
    origNewTap := newTapFunc
    defer func() { newTapFunc = origNewTap }()
    
    // replace with mock
    newTapFunc = func(...) (*tapDevice, error) {
        return &tapDevice{...}, nil
    }
    
    // run test
}
```

### 可 mock 的函数

| 函数变量 | 文件 | 作用 |
|----------|------|------|
| `newTapFunc` | `tap_lifecycle.go` | 创建 TAP 设备 |
| `restoreTapFunc` | `tap_lifecycle.go` | 恢复 TAP 设备 |
| `cubevsAttachFilter` | `local_service.go` | 附加 eBPF 过滤器 |
| `cubevsAddTAPDevice` | `tap_lifecycle.go` | 注册 TAP 到 cubevs |
| `cubevsRemoveTAPDevice` | `tap_lifecycle.go` | 从 cubevs 移除 TAP |
| `cubevsSetPortMapping` | `tap_lifecycle.go` | 设置端口映射 |
| `cubevsClearPortMapping` | `tap_lifecycle.go` | 清除端口映射 |

### 测试辅助

```go
// 使用临时目录
func TestStateStore(t *testing.T) {
    dir := t.TempDir()  // 自动清理
    store := newStateStore(dir)
    // ...
}

// 并行测试
func TestConfig(t *testing.T) {
    t.Parallel()
    // ...
}
```

---

## 本地运行

### 基础运行

```bash
# 需要 root 权限
sudo ./bin/network-agent --eth-name=eth0
```

### 开发模式

```bash
# 使用 go run
sudo go run ./cmd/network-agent --eth-name=eth0

# 指定监听地址 (避免冲突)
sudo go run ./cmd/network-agent \
    --eth-name=eth0 \
    --listen=unix:///tmp/dev-network-agent.sock \
    --grpc-listen=unix:///tmp/dev-network-agent-grpc.sock \
    --state-dir=/tmp/dev-network-agent-state
```

### 测试 API

```bash
# 健康检查
curl --unix-socket /tmp/dev-network-agent.sock http://localhost/healthz

# 创建网络
curl -X POST --unix-socket /tmp/dev-network-agent.sock \
    http://localhost/v1/network/ensure \
    -H "Content-Type: application/json" \
    -d '{"sandboxID": "test-1"}'
```

---

## 代码规范

### 格式化

```bash
# 格式化代码
go fmt ./...

# 或使用 gofumpt (更严格)
gofumpt -w .
```

### Lint

```bash
# 使用 golangci-lint
golangci-lint run ./...
```

### 建议的 lint 配置

```yaml
# .golangci.yml
linters:
  enable:
    - errcheck
    - gosimple
    - govet
    - ineffassign
    - staticcheck
    - typecheck
    - unused
    - gofmt
    - goimports
```

---

## 依赖管理

### 更新依赖

```bash
# 更新所有依赖
go get -u ./...
go mod tidy

# 更新特定依赖
go get -u github.com/vishvananda/netlink
```

### 本地依赖 (cubevs)

项目使用 `replace` 指令引用本地的 `cubevs`:

```go
// go.mod
replace <current-cubevs-module-path> => ../mvs/cubevs
```

开发时确保 `cubevs` 目录存在于正确位置。

### 查看依赖

```bash
# 查看直接依赖
go list -m all

# 查看依赖图
go mod graph
```

---

## 调试

### 使用 delve

```bash
# 安装 delve
go install github.com/go-delve/delve/cmd/dlv@latest

# 调试
sudo dlv debug ./cmd/network-agent -- --eth-name=eth0
```

### 日志

项目使用标准 `log` 包。可通过添加环境变量增强日志:

```bash
# 示例 (需要在代码中实现)
DEBUG=1 sudo ./bin/network-agent --eth-name=eth0
```

### 跟踪系统调用

```bash
# 使用 strace
sudo strace -f ./bin/network-agent --eth-name=eth0
```

---

## 贡献流程

### 1. 创建分支

```bash
git checkout -b feature/my-feature
```

### 2. 开发

- 编写代码
- 添加测试
- 运行测试确保通过

### 3. 提交

```bash
# 格式化和检查
go fmt ./...
go vet ./...
go test ./...

# 提交
git add .
git commit -m "feat: add my feature"
```

### 4. 代码审查

- 提交 Merge Request
- 等待代码审查
- 根据反馈修改

---

## 常见问题

### Q: 测试需要 root 权限吗?

A: 大部分单元测试不需要，因为使用了 mock。集成测试可能需要。

### Q: 如何在没有 cubevs 的环境测试?

A: 使用 mock 函数替换 cubevs 相关调用。参考 `local_service_test.go`。

### Q: 如何添加新的 API?

1. 修改 `api/v1/network_agent.proto`
2. 运行 protoc 重新生成代码
3. 在 `internal/service/` 添加业务逻辑
4. 在 `internal/httpserver/` 和 `internal/grpcserver/` 添加路由
5. 添加测试

### Q: 如何调试 eBPF 问题?

A: 使用 `bpftool` 查看 eBPF maps 和程序:

```bash
# 查看 maps
sudo bpftool map list

# 查看 map 内容
sudo bpftool map dump id <map_id>

# 查看程序
sudo bpftool prog list
```
