# compose-carry

**搬迁 Docker Compose 服务前，核对目标主机缺少哪些路径依赖。**

[English](README.md)

把 Compose 文件复制过去，不代表宿主机上的配置文件、密钥文件和挂载目录也齐全。本工具对照源机快照，报告目标机的路径缺失、类型不符与未验证项。

v0.4 是早期命令行版本：Linux 主机路径扫描、跨平台离线比较，以及可选的 Docker CLI 外部资源检查；只有 resources 命令需要 Docker CLI 访问 daemon。它不是备份工具、迁移执行器或 Compose lint；检查通过**不保证应用能启动、数据完整或权限正确**。

## 安装发行包

二进制包须在发行草稿审核后公开。如果 [Releases 页面](https://github.com/565431hzhang/compose-carry/releases) 尚无公开附件，请使用下方源码构建；GitHub 自动生成的源码压缩包不是可执行程序。

| 系统 | 包名后缀 | 支持范围 |
| --- | --- | --- |
| Linux x86-64 | `linux_amd64.tar.gz` | 全部命令 |
| Linux ARM64 | `linux_arm64.tar.gz` | 全部命令；仅交叉编译，尚未硬件实测 |
| Windows x86-64 | `windows_amd64.zip` | 离线比较和 Docker 资源查询，不支持路径扫描 |

从同一发行版本下载所需压缩包与 `SHA256SUMS`，放到新的空目录。Linux 先校验再解压（将 VERSION、ARCH 换成实际版本、架构）：

```sh
sha256sum --check --ignore-missing SHA256SUMS
mkdir unpacked
tar -xzf compose-carry_VERSION_linux_ARCH.tar.gz -C unpacked
mkdir -p "$HOME/.local/bin"
install -m 0755 unpacked/compose-carry "$HOME/.local/bin/compose-carry"
"$HOME/.local/bin/compose-carry" --version
```

必须看到所下载包的 `OK`；报错或没有检查任何包都不能继续。需要时将 `$HOME/.local/bin` 加入 shell PATH，无需 root。

Windows 对照 `SHA256SUMS` 中相同文件名的哈希，完全一致后再解压安装：

```powershell
Get-FileHash .\compose-carry_VERSION_windows_amd64.zip -Algorithm SHA256
Get-Content .\SHA256SUMS
Expand-Archive .\compose-carry_VERSION_windows_amd64.zip -DestinationPath .\unpacked
New-Item -ItemType Directory -Force "$HOME\AppData\Local\Programs\compose-carry"
Copy-Item .\unpacked\compose-carry.exe "$HOME\AppData\Local\Programs\compose-carry\compose-carry.exe"
& "$HOME\AppData\Local\Programs\compose-carry\compose-carry.exe" --version
```

可在 Windows 环境设置中将安装目录加入用户 PATH。`--version` 显示版本与提交号；校验和用于检查下载损坏，不等同于数字签名或发布者身份认证。

升级时关闭程序、保留旧可执行文件备份，校验新包后只替换已安装程序（PowerShell 可能需要 `Copy-Item -Force`）。卸载只删除已安装程序及自己添加的 PATH 项；不会删除快照或配置。

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

ARM64 主机改用 `GOARCH=arm64`。扫描必须直接在 Docker daemon 所在的 Linux 主机及相应文件系统命名空间中执行；不能在普通容器里或仅通过远程 Docker context 的客户端检查。Windows/macOS 不支持路径扫描，可离线比较；resources 则依赖可用的 Docker CLI/context，不依赖本地 Linux 路径访问。

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

路径流程不连接 SSH、不复制数据、不创建缺失路径、不部署服务。只写命令明确指定的快照文件；已有输出一律拒绝覆盖。

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

## 聚焦问题与排除项策略

```sh
./compose-carry compare --source source.json --target target.json --only-problems
./compose-carry compare --source source.json --target target.json --only-problems --fail-on-excluded --format json
```

报告新增全量 `summary`，固定包含 matched、target_missing、type_mismatch、source_unverified、target_unverified、excluded 六类计数，零值也保留。按依赖条目计数，不按唯一路径去重；`checked` 是范围内条目数，不是成功数。

`--only-problems` 只隐藏 matched 行，排除项仍显示；全量汇总、计数和退出判定不受过滤影响。JSON 显式包含 `only_problems`、`fail_on_excluded`，过滤后的空 `results` 是 `[]`，不代表没有执行检查。文本先显示全量汇总，再列条目并说明显示数量。

`--fail-on-excluded` 在存在**已记录**的排除项时返回 `1`，即使范围内路径全部匹配。默认策略不变。两个选项仅用于 compare，不改变 source/target 行为和 v1/v2 快照格式。网络等未枚举资源仍未检查，不能将该选项当作完整迁移准入检查。

JSON 成功写出时，`exit_code` 与进程退出码一致；输出故障仍返回 `2`，部分写出的报告应丢弃。

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

## 外部 Docker 卷与网络

```sh
./compose-carry resources --config compose.json --docker-context destination --format json
```

必须显式选择已有 Docker context（允许 `default`）。查询子进程会移除 DOCKER_HOST、DOCKER_CONTEXT、DOCKER_TLS、DOCKER_TLS_VERIFY、DOCKER_CERT_PATH 端点覆盖变量，保留 context 存储和凭据，不修改系统环境。这是独立命令：读取已渲染的 Compose JSON，通过 Docker CLI 查询该 context 的 daemon，可能访问远程主机；不读取或修改路径快照、不合并报告、不传输配置，也不证明所选 daemon 与路径扫描主机一致。渲染配置仍可能含密码，不要为了该命令随意传输或公开它。

仅检查服务实际引用的 `external: true` 命名卷和网络。顶层 `name` 优先于逻辑键，不添加项目名前缀；按资源种类和名称合并重复引用，保留服务/逻辑键关联。隐式外部 default 网络会纳入，但 `network_mode` 服务不添加默认网络。普通 Compose 管理资源、匿名卷、未引用定义不检查；输入须使用布尔 external 的渲染 JSON，不支持旧式 external 对象。

每个需要的类别只执行一次只读清单查询，只投影卷名及网络名/完整 ID。卷按完整名称匹配；网络支持名称、完整 ID 或唯一 ID 前缀。重名或名称/ID 冲突保守标为 `unverified`，不猜 Docker 解析优先级。含 `$` 或控制字符的名称保留为未验证。

结果为 `present`、`missing`、`unverified`，附汇总与原因码。只有成功解析的清单里没有候选才算缺失；CLI 不存在、查询失败、超时、输出超限或格式错误不能算缺失。每类查询15秒截止、stdout 上限16 MiB、退出后管道等待上限1秒，原始 stderr 不回显；报告只包含被引用资源，不泄露无关清单。

退出码：至少一项且全部存在为 `0`，缺失/未验证/无项为 `1`，参数、输入或报告写入错误为 `2`；无项时不调用 Docker。存在不代表卷内容、驱动、子网、连接性、权限或应用可用。Docker context 及传输助手由用户管理，须可信；该命令不会启动容器或修改 Docker 资源。

### 资源查询诊断

查询故障仍为 `unverified`、退出 `1`；JSON 和文本都增加固定 `hint`，不回显原始 stderr、部分 stdout 或异常详情。v0.4.1 将原因细分为：

| 原因码 | 排查方向 |
| --- | --- |
| `command_not_found` | 安装 Docker CLI，检查 PATH。 |
| `timeout` | 检查选定 context 和 daemon 连通性后重试。 |
| `output_limit` | 清单 stdout 超过16 MiB，在本地检查清单规模。 |
| `command_failed` | 用 Docker CLI 在本地检查 context、访问权限和凭据。 |
| `query_canceled` | 本次已取消，准备好后重试；不是超时。 |
| `io_error` | 检查程序执行权限、进程或管道故障。 |
| `query_failed` | 未分类故障，在本地查看 Docker 诊断。 |
| `invalid_inventory` | 检查 Docker CLI 兼容性及清单投影格式。 |
| `ambiguous_reference` | 使用无歧义的完整 ID/名称，重新导出 Compose JSON。 |
| `unresolved_name` | 解析 Compose 变量并核查资源名称。 |

`not_found` 仍代表有效清单里没有候选，现在附带核对名称/context 的建议；`found` 不添加提示。`command_failed` 不等于“daemon离线”或“没有权限”，具体原因需要在本地查看 Docker 自身报错。原状态、退出码和路径快照格式不变；调用方遇到将来新增原因码不能直接当成成功。

## 路径流程核对范围

- Compose 渲染后的长语法 bind mount；预期文件/目录类型来自源机实际元数据，不靠后缀猜测。
- 服务实际引用的文件型 configs/secrets，要求普通文件；不检查未引用定义。
- 路径本身及任意祖先含符号链接时均不跟随。特殊文件、元数据访问受限、源路径缺失、相对或未解析路径均不能标为通过。
- 快照严格校验版本、字段名、依赖 ID、重复项、状态组合、完整依赖集合及源机证据；不只比较两份文件的交集。

命名卷、非 bind 挂载、external/environment/inline 等来源记录为排除项；网络等其他 Compose 功能不检查，也不逐项列入清单。默认只要范围内的项目全部匹配，存在排除项本身不会导致非零退出码；compare 可用 `--fail-on-excluded` 改变该策略。

路径含 `$` 或反斜杠时，即使是合法字面字符，也保守标为未验证；需要规范化的路径同样不扫描。本工具不替代完整 Compose schema 校验，请输入 Compose 导出的有效 JSON，而不是原始 YAML 或手写短语法。

## 路径流程退出码

- `0`：至少有一项范围内依赖，范围内全部检查成功（target/compare 还要求类型匹配）。
- `1`：有缺口、类型不符、未验证项、没有可检查路径，或 compare 指定 `--fail-on-excluded` 时存在已记录排除项。
- `2`：参数、输入、快照无效，平台不支持扫描，或命令级输入输出错误。

扫描返回 `1` 时仍会写出有效快照，用于继续诊断。需要在有缺口时继续生成报告，不要简单用 `&&` 串联整个流程。再次扫描请使用新输出文件名。

## 隐私与限制

- 路径流程不联网、不递归目录、不读取依赖文件内容、不修改被扫描路径；resources 则显式查询 Docker。Linux 使用只获取元数据的文件句柄，逐级拒绝符号链接。
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

该测试只运行 `docker compose config`，不启动容器或拉镜像。原生 Linux CI 在 AMD64、ARM64 上执行 race、Compose 导出、Docker 资源检查和安装包冒烟测试；Windows 测试覆盖离线比较和拒绝扫描。

显式在可用于测试的 Docker daemon 上验收资源检查：测试会创建唯一命名的空卷和内部网络，按记录的名称/ID 删除并确认清理，不启动容器。

```sh
COMPOSE_CARRY_TEST_RESOURCES=1 go test ./cmd/compose-carry -run TestDockerResourcesIntegration -v
```

资源集成测试在本地须显式启用，在一次性原生 Linux CI runner 上明确启用；无 daemon 单元测试覆盖查询失败、歧义、输出上限和截止时间。

### 原生架构与迁移 CI

两个源端 job 使用标准 `ubuntu-24.04` 和 `ubuntu-24.04-arm` 并行运行。每个 job 启动独立的源端、目标端特权 DinD 容器；扫描程序在对应文件系统命名空间内以 UID 1000、零有效 capabilities 执行，只有 daemon 设置需要特权。覆盖缺失、类型不符、修复、映射、链接、权限拒绝和两端外部资源差异。不挂宿主 Docker socket，不发布 daemon TCP 端口。

随后两个目标端 job 并行，分别在原生 ARM64 检查 AMD64 生成的源快照，以及反方向核对。仅通过 job outputs 传递编码后不超过16 KiB的合成快照，输出缺失、架构或提交不匹配直接失败；不用 QEMU 或外部中转。这是容器隔离测试，不等同于两台物理机。

所有 job 要求公开仓库，只使用标准 runner；Linux 最长25分钟、Windows 最长10分钟。关闭缓存，不上传 Actions artifacts 或持久构建缓存；安装包只在 runner 临时生成，测试后删除。DinD 容器及匿名卷由 finally 和 always 步骤清理；硬取消后由 GitHub 销毁临时 runner。显式标签发行附件与测试产物分开。免费依赖 GitHub 的公开仓库标准 runner 政策，不代表能控制账号其他工作流或费用。

## 打包与发行审核

在已提交、无未提交改动的仓库根目录执行，Go 和 Git 须在 PATH 中：

```sh
go run ./cmd/package-release --out dist
COMPOSE_CARRY_RELEASE_DIR=../../dist go test ./cmd/package-release -run TestReleaseArtifacts -count=1 -v
```

`dist` 必须不存在。生成三个压缩包与独立的 `SHA256SUMS`，每包仅含程序、许可证和两份 README。版本单源为 `internal/buildinfo/version.go`，传入 `--version vX.Y.Z` 时必须一致。源码构建显示 `commit dev`，发行包嵌入实际提交号。压缩包元数据和顺序固定，但不承诺不同 Go 版本间逐字节可重复。

标签发行工作流只接受与源码版本一致的 `vMAJOR.MINOR.PATCH`，等待 Linux/Windows 测试通过后创建**草稿**。不会自动推送标签或覆盖已有发行；人工检查说明和附件后再公开。CI 配置为核对包内容、哈希并执行原生 Windows amd64、Linux amd64/arm64 程序；实际验证以对应工作流结果为准，不重写历史发行说明。

## 后续方向

显式路径映射和外部资源存在性检查已实现；后续按实际搬迁案例增加更丰富的资源元数据，以及边界明确的权限诊断，不扩张成部署平台。

[Troubleshoot](https://github.com/replicatedhq/troubleshoot)、[DCLint](https://github.com/zavoloklom/docker-compose-linter)、[Compose Volume Check](https://github.com/Profen-BLF/compose-volume-check) 已解决相邻问题；本项目聚焦源机依赖与目标机实际路径的核对，不宣称首创或替代这些项目。

MIT 许可证。开发包含 AI 协助，仍需审阅和测试。
