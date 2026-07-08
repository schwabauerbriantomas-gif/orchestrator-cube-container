# Proto Generation (Draft)

当前 `network_agent.proto` 作为 `network-agent` 与 `Cubelet` 共享的 contract 来源。

重新生成代码前，请先确保本机已安装所需工具：

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

PATH="$(go env GOPATH)/bin:$PATH"
```

然后在 `network-agent` 目录执行：

```bash
make proto
```

注意：

- `go_package` 已对齐 `go.mod` 模块路径：`github.com/tencentcloud/CubeSandbox/network-agent`。
- `make proto` 会同时更新 `api/v1/` 下的服务端代码，以及 `../Cubelet/pkg/networkagentclient/pb/` 下的客户端副本，避免 contract 漂移。
