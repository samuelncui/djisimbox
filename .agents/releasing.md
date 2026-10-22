# 构建与发布

Zero 2W 使用 `linux/arm64` 静态二进制，通用 64 位 Linux 主机可使用 `linux/amd64`。运行目标机器无需安装 Go；ModemManager、systemd 和配置要求见[安装文档](../docs/install.zh-CN.md)。

## CI 与下载

`.github/workflows/ci.yml` 在 PR、`main` 推送和手动运行时执行同一条验证与打包流程：

1. 以 `fetch-depth: 0` 获取全部历史，通过 `bash scripts/check-secrets.sh` 检查工作目录和所有已获取 Git refs 的历史；脚本固定 Gitleaks 版本和下载摘要，输出脱敏。然后检查 Go 格式、依赖完整性、`go vet` 和 `go test -race -count=1 ./...`。测试使用独立的 D-Bus 守护进程与合成数据，无需真实 modem、Telegram bot 或主机 `/etc` 配置。
2. 验证成功后，以 `CGO_ENABLED=0`、`-trimpath`、`-buildvcs=false` 编译 ARM64 和 amd64。检查包内 ELF 架构、动态解释器与动态库依赖，并在 amd64 构建机执行包内 `djisimbox version`。
3. 上传名为 `djisimbox-dev-<12 位提交 SHA>-linux` 的 Actions artifact，保留 14 天；下载步骤见[安装指南](../docs/install.zh-CN.md#1-下载程序)。手动运行不会创建 Release。

工作流使用 `go.mod` 要求的 Go 1.26 系列最新可用补丁。`verify`、`build` 只有 `contents: read`，checkout 不保留凭证；只有最后的 `release` 作业具有 `contents: write`，它只下载同次构建的 artifact 并调用 GitHub CLI。官方 Actions 均固定完整提交 SHA，旁注标明核验过的版本；更新时一起核对上游发布与 SHA。

## 标签发布

首次公开发布前，先完成隐私检查、文档审阅和目标硬件验证。准备发布时，由维护者审阅并推送指向所需提交的 `v` 前缀语义版本标签，例如 `v0.1.0` 或 `v0.1.0-rc.1`。

隐私检查覆盖待发布分支和标签的全部历史，包括文件、提交说明、作者与提交者信息。除密钥扫描外，人工复核 IP/MAC、设备 SN、IMEI/IMSI/ICCID、电话号码与 Telegram 收件人标识，区分公开占位值和真实设备信息。提交署名使用维护者认可的用户名和邮箱。清理当前文件、忽略规则或 squash 都不能代替检查已存在的远端提交与其他 refs。

标签触发完整验证与构建；版本格式不合法、验证或打包失败时都不会创建 Release。成功后，工作流用已有标签创建 GitHub Release、生成变更说明，并上传：

```text
djisimbox_v0.1.0_linux_arm64.tar.gz
djisimbox_v0.1.0_linux_amd64.tar.gz
SHA256SUMS
```

二进制的 `djisimbox version` 返回同一标签。带预发布部分的标签标记为 prerelease。工作流使用 `gh release create --verify-tag`，标签必须已存在；重跑已发布标签不会覆盖既有 Release 或资产。发布中断时，先检查是否留下草稿及已上传文件，再由维护者处理并重跑。普通 PR、分支或手动构建不会发布。

## 运行包边界

每个压缩包有同名根目录，文件清单固定在 `scripts/build-release.py` 的 `SOURCE_FILES`：

- `djisimbox`、`README.md`、`README.zh-CN.md`、项目 `LICENSE`。
- `deploy/` 中的五个随项目维护的 systemd、polkit、networkd 文件。
- `examples/config.json`、`examples/secrets.json`，只包含公开占位值。
- `docs/install.md`、`operations.md` 及对应的 `*.zh-CN.md`。
- `THIRD_PARTY_NOTICES`，包含构建所用 Go 运行时及内置依赖的许可证、PATENTS，以及 `go.mod` 固定的 godbus/dbus、x/sys 许可证。

脚本逐个复制清单中的普通文件，新增文件需主动加入清单。运行配置、凭证、运行状态、`.git`、开发记录和研究文档不会因位于工作目录而自动入包。依赖许可证从 `go mod download` 后通过 `go mod verify` 的模块目录读取；新增、删除或本地替换依赖时，先审阅并更新许可证清单。压缩包元数据统一为固定时间、UID/GID 和文件权限；二进制保留执行权限。

GitHub 自动生成的 Source code 压缩包是标签对应的公开仓库源码，与上述运行包不同；仓库公开前仍需审阅所有将提交的文件和历史。

## 本地复现

在 Linux amd64（x86_64）上准备 Go 1.26 或更新版本、Python 3、curl、binutils（提供 `readelf`）、`dbus-daemon` 和 C 编译工具链（Debian/Ubuntu 为 `build-essential`），从仓库根目录运行：

```sh
command -v dbus-daemon
bash scripts/check-secrets.sh
test -z "$(gofmt -l cmd internal)"
go mod download
go mod verify
go vet ./...
go test -race -count=1 ./...
python3 scripts/build-release.py v0.1.0 --output-dir dist
cd dist
sha256sum --check --strict SHA256SUMS
tar -tzf djisimbox_v0.1.0_linux_arm64.tar.gz
```

将示例版本替换为实际版本；开发构建可使用 `dev-` 加提交 SHA 的前 12 位。输出目录必须为空，脚本无需主机凭证、真实设备或 bot。ARM64 包的实际启动、USB 热插拔、多 modem 和真实短信/来电仍需在目标设备验证，交叉编译不能替代这些检查。

维护参考：[GitHub 最小权限与输入处理](https://docs.github.com/en/actions/reference/security/secure-use)、[上传 artifact](https://github.com/actions/upload-artifact)、[下载 artifact](https://github.com/actions/download-artifact)、[GitHub CLI 发布参数](https://cli.github.com/manual/gh_release_create)。
