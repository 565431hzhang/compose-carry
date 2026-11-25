# compose-carry

**搬迁 Docker Compose 服务前，核对目标主机缺少哪些路径依赖。**

[English](README.md)

把 Compose 文件复制过去，不代表宿主机上的配置文件、密钥文件和挂载目录也齐全。本工具对照源机快照，报告目标机的路径缺失、类型不符与未验证项。

v0.2 是早期命令行版本：Linux 主机扫描、跨平台离线比较、无运行时第三方依赖。它不是备份工具、迁移执行器或 Compose lint；检查通过**不保证应用能启动、数据完整或权限正确**。

## 构建

需要 Go 1.24 或更新版本，在仓库根目录执行：

```sh
go build -o compose-carry ./cmd/compose-carry
./compose-carry --help
```

其他平台可以交叉编译 Linux 版：

```sh
GOOS=linux GOARCH=amd64 go build -o compose-carry ./cmd/compose-carry
```

ARM64 主机改用 `GOARCH=arm64`。扫描必须直接在 Docker daemon 所在的 Linux 主机及相应文件系统命名空间中执行；不能在普通容器里或仅通过远程 Docker context 的客户端检查。Windows/macOS 只能做离线比较。

## 使用流程

### 1. 在源机导出配置并生成快照

使用与实际部署一致的 `-f`、环境、项目目录和 profile 参数；不要使用 `--no-path-resolution` 或 `--no-interpolate`。

```sh
umask 077
docker compose config --format json > compose.json
./compose-carry source --config compose.json --out source.json
```

**导出的 Compose JSON 可能含密码，留在源机，不要提交或分享。** 程序只从中提取核对必需的元数据，不把环境变量、内联配置或依赖文件内容带入快照。

### 2. 在目标机检查

通过你自己的安全传输方式带上 `source.json` 和匹配架构的程序。先检查快照里的路径；默认检查相同绝对路径，新旧目录不同时可使用下面的显式映射。

```sh
./compose-carry target --manifest source.json --out target.json
```

工具不连接 SSH、不复制数据、不创建缺失路径、不部署服务。只写命令明确指定的快照文件；已有输出一律拒绝覆盖。

### 3. 离线比较

将两份快照放在同一台机器：

```sh
./compose-carry compare --source source.json --target target.json
./compose-carry compare --source source.json --target target.json --format json
```

| 状态 | 含义 |
| --- | --- |
| `matched` | 路径存在且文件/目录类型符合源机要求，内容与权限未验证 |
| `target_missing` | 目标路径缺失 |
| `type_mismatch` | 目标路径或其祖先类型不符 |
| `source_unverified` | 源机无法建立有效基准，目标机存在也不能算通过 |
| `target_unverified` | 目标机存在符号链接、权限限制或其他无法确定的情况 |
| `excluded` | 不在本版核对范围，不计为已验证 |

JSON 报告包含核对范围、范围内条目数、排除条目数、退出码以及每项的预期类型和两端观察结果。

## 新旧机器目录不同

在目标机准备私有 `paths.json`，明确写出源路径和目标路径：

```json
{
  "/srv/app/data": "/mnt/app/data",
  "/srv/app/config.json": "/etc/app/config.json"
}
```

```sh
./compose-carry target --manifest source.json --path-map paths.json --out mapped.json
./compose-carry compare --source source.json --target mapped.json
```

只做**完整路径精确匹配**：映射 `/srv/app` 不会替换 `/srv/app/data`。键必须是清单内的范围内依赖，两侧必须是规范 Linux 绝对路径；重复键、未知源路径、非字符串和非法路径在扫描前拒绝。同一路径的多个引用共享映射，未映射项仍检查源路径位置；原本非法的源路径继续未验证，不能借映射修复。

文本报告显示 `source_path` 和 `target_path`；JSON 保留 `dependency.path` 作为源路径并新增 `target_path`。源机身份和证据不会改写。工具**不会修改 Compose、搬运文件或确认 Compose 已改用新路径**；请审核映射和报告，匹配仅针对指定路径。

不传映射仍生成 v1 快照；传入映射（包括 `{}`）生成仅用于 target 的 v2 快照，每项必须记录 `target_path`（排除项为空）。未映射的非法路径保留为未验证，不自动规范化。新版能比较原有 v1 快照，v0.1 会拒绝 v2；映射检查和比较都应使用新版程序。映射文件也有敏感路径元数据，受 16 MiB 输入限制。

## 核对范围

- Compose 渲染后的长语法 bind mount；预期文件/目录类型来自源机实际元数据，不靠后缀猜测。
- 服务实际引用的文件型 configs/secrets，要求普通文件；不检查未引用定义。
- 路径本身及任意祖先含符号链接时均不跟随。特殊文件、元数据访问受限、源路径缺失、相对或未解析路径均不能标为通过。
- 快照严格校验版本、字段名、依赖 ID、重复项、状态组合、完整依赖集合及源机证据；不只比较两份文件的交集。

命名卷、非 bind 挂载、external/environment/inline 等来源记录为排除项；网络等其他 Compose 功能不检查，也不逐项列入清单。只要范围内的项目全部匹配，存在排除项本身不会导致非零退出码。

路径含 `$` 或反斜杠时，即使是合法字面字符，也保守标为未验证；需要规范化的路径同样不扫描。本工具不替代完整 Compose schema 校验，请输入 Compose 导出的有效 JSON，而不是原始 YAML 或手写短语法。

## 退出码

- `0`：至少有一项范围内依赖，范围内全部检查成功（target/compare 还要求类型匹配）。
- `1`：有缺口、类型不符、未验证项，或没有可检查路径。
- `2`：参数、输入、快照无效，平台不支持扫描，或命令级输入输出错误。

扫描返回 `1` 时仍会写出有效快照，用于继续诊断。需要在有缺口时继续生成报告，不要简单用 `&&` 串联整个流程。再次扫描请使用新输出文件名。

## 隐私与限制

- 不联网、不递归目录、不读取依赖文件内容、不修改被扫描路径。Linux 使用只获取元数据的文件句柄，逐级拒绝符号链接。
- 必须读取用户明确指定的配置和快照输入，单个输入及生成快照上限均为 16 MiB，超大快照在创建输出前拒绝。Linux 输出快照权限为 `0600`；终端输出或重定向报告的权限由 shell 决定。
- 路径、服务名称和引用仍可能敏感，不要公开真实快照；没有通用脱敏和签名功能。
- 快照只是一个时间点的观察，不证明主机身份、时效或真实性。依赖 ID 用于发现不一致，不防范协调篡改；依赖与源证据完全一致的不同主机快照仍可兼容，用户须选对文件。
- 不验证 UID/GID、ACL、SELinux、容器访问权限、挂载标志、文件内容、命名卷内容、网络、镜像、数据库一致性或应用可用性。
- 先检查不可信清单，不要无必要地用 root 扫描，并使用自己控制的输入/输出目录。故障网络文件系统可能令内核操作等待；不承诺扫描超时。

## 测试

```sh
go test ./...
go vet ./...
```

Linux 测试覆盖缺失、类型变化、祖先符号链接、FIFO、非 root 权限错误和实际二进制完整流程。端到端测试通过修改临时路径来模拟两台主机的差异，不会连接真实服务器。可选的真实 Compose 导出兼容测试：

```sh
COMPOSE_CARRY_TEST_COMPOSE=1 go test ./cmd/compose-carry -run TestComposeExport -v
```

只运行 `docker compose config`，不启动容器或拉镜像。CI 在非 root Linux 上执行扫描、Compose 导出集成测试及 amd64/arm64 编译；Windows 测试覆盖离线比较和拒绝扫描。

## 后续方向

显式路径映射已实现；后续按实际搬迁案例增加外部卷/网络元数据，以及边界明确的权限诊断，不扩张成部署平台。

[Troubleshoot](https://github.com/replicatedhq/troubleshoot)、[DCLint](https://github.com/zavoloklom/docker-compose-linter)、[Compose Volume Check](https://github.com/Profen-BLF/compose-volume-check) 已解决相邻问题；本项目聚焦源机依赖与目标机实际路径的核对，不宣称首创或替代这些项目。

MIT 许可证。开发包含 AI 协助，仍需审阅和测试。
