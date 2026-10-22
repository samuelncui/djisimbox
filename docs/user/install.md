# 安装到 Orange Pi Zero 2W

以下步骤面向使用 systemd-networkd 的 Debian/Armbian ARM64 系统，要求 systemd 支持日志命名空间、cgroup v2 和 tmpfs `/run`。先准备可用的 Wi-Fi/SSH、足够的 USB 供电与数据连接，以及已解锁并开通所需短信/来电服务的 SIM。程序和全部运行依赖都安装在板上。

首次安装时先断开蜂窝模块，完成主机网口保护后再连接。若系统使用 NetworkManager 或其他网络管理器，先配置并验证等价的持久保护；仓库提供的 `.network` 文件只由 systemd-networkd 执行。

## 1. 取得程序与部署文件

预编译程序不需要 Go。通过 Wi-Fi 安装下载工具、HTTPS 根证书和运行依赖：

```sh
sudo apt-get update
sudo apt-get install --no-install-recommends ca-certificates curl modemmanager polkitd
```

**目前尚未发布首个版本。** 以下下载命令在首个 GitHub Release 发布后适用。把 `VERSION` 换成真实标签，保留 `v` 前缀；Zero 2W 使用 `arm64`（`uname -m` 应为 `aarch64`），Linux x86-64 使用 `amd64`，硬件兼容性仍需自行验证。

```sh
VERSION=vX.Y.Z
ARCH=arm64
ASSET="djisimbox_${VERSION}_linux_${ARCH}.tar.gz"
RELEASE_URL="https://github.com/samuelncui/djisimbox/releases/download/${VERSION}"
curl --fail --location --remote-name "${RELEASE_URL}/${ASSET}"
curl --fail --location --remote-name "${RELEASE_URL}/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
```

校验必须显示所选 tar 包 `OK`。`SHA256SUMS` 用于检查下载完整性；应从项目自己的 Release 取得文件。校验通过后解包：

```sh
tar -xzf "$ASSET"
cd "djisimbox_${VERSION}_linux_${ARCH}"
```

每包包含 `djisimbox`、部署文件、示例配置、文档与许可证。下文所有相对路径均以解包目录为准。

首个 Release 前，可从项目 [Actions](https://github.com/samuelncui/djisimbox/actions) 的成功运行下载 `djisimbox-dev-<12位提交摘要>-linux` 构建产物，先解开 artifact 的 ZIP，再校验其中的 tar 包。使用包名中实际的版本字段（如 `dev-<12位提交摘要>`）设置 `VERSION`，设好 `ARCH` 与 `ASSET`，执行上面的校验、解包与 `cd` 步骤。也可按[源码构建说明](https://github.com/samuelncui/djisimbox/blob/main/docs/development/architecture.md#源码构建与验证)生成 `bin/djisimbox-linux-arm64`，在源码根目录继续操作，将后面的二进制来源 `djisimbox` 替换为 `bin/djisimbox-linux-arm64`。

## 2. 先保护蜂窝网口

确认 systemd-networkd 已管理目标接口，再安装规则：

```sh
sudo install -m 0644 deploy/networkd/05-djisimbox-sms-only.network /etc/systemd/network/
sudo networkctl reload
```

该规则按 USB 身份 `2c7c:0125` 匹配已验证的 DJI 模块，令其主机网口始终关闭，禁用 DHCP、IPv6 RA 与链路本地地址。其他 USB 身份需要相应规则。networkd 采用首个匹配的配置文件，检查是否有排序更早的规则覆盖它。

连接模块后，用 `networkctl list` 找到它的网口名；将下例的值替换为实际名称：

```sh
MODEM_IFACE=替换为模块网口名
sudo networkctl reconfigure "$MODEM_IFACE"
networkctl status "$MODEM_IFACE" --no-pager
ip -br address show dev "$MODEM_IFACE"
ip route show default
ip -6 route show default
```

确认网口匹配 `05-djisimbox-sms-only.network`、处于关闭状态、没有 IPv4/IPv6 地址，默认路由使用独立网络。应用会拒绝已开启或带地址的蜂窝主机接口；它只检查，不会替代网络配置。该保护保留 USB 串口短信功能，无法约束模块内部 IMS 通信或运营商计费。

## 3. 安装账户、程序和配置

`useradd` 只需首次执行；升级时复用已有专用账户。

```sh
sudo useradd --system --user-group --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin djisimbox
sudo install -m 0755 djisimbox /usr/local/bin/djisimbox
sudo install -d -m 0750 -o root -g djisimbox /etc/djisimbox
sudo install -m 0600 -o djisimbox -g djisimbox examples/config.json /etc/djisimbox/config.json
sudo install -m 0600 -o djisimbox -g djisimbox examples/secrets.json /etc/djisimbox/secrets.json
sudoedit /etc/djisimbox/config.json /etc/djisimbox/secrets.json
```

按[配置说明](configuration.md)替换全部示例值。保持 token 文件私有；升级时保留现有配置和凭据。

## 4. 安装权限、日志与服务

```sh
sudo install -m 0644 deploy/polkit/50-djisimbox.rules /etc/polkit-1/rules.d/
sudo install -m 0644 deploy/systemd/djisimbox.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/journald@djisimbox.conf /etc/systemd/
sudo install -d -m 0755 /etc/systemd/system/systemd-journald@djisimbox.service.d
sudo install -m 0644 deploy/systemd/journald-djisimbox-volatile.conf /etc/systemd/system/systemd-journald@djisimbox.service.d/50-volatile.conf
sudo systemctl daemon-reload
sudo systemctl enable --now ModemManager
sudo mmcli -L
```

若首次安装时模块已经连接、ModemManager 没有识别到串口，可刷新设备属性后重新检查：

```sh
sudo udevadm trigger --action=change --subsystem-match=tty --sysname-match='ttyUSB*'
sudo udevadm settle
sudo mmcli -L
```

Polkit 规则授予专用账户 ModemManager 的 `Messaging` 和 `Device.Control` 权限，系统授权粒度比程序使用范围更大。程序只使用接收、短信原件删除与必要的模块启用操作；来电通过只读属性观察。

服务使用独立用户和私有运行目录，禁用该服务的 swap 与 core dump。日志写入有容量限制的 volatile journal 命名空间；配套 drop-in 清除持久日志目录。上述设置覆盖本服务及其日志，操作系统和模块仍有各自的存储行为。

## 5. 检查并启动

```sh
sudo -u djisimbox /usr/local/bin/djisimbox check
sudo systemctl enable --now djisimbox
sudo -u djisimbox /usr/local/bin/djisimbox status
sudo journalctl --namespace=djisimbox -u djisimbox --no-pager -n 30
```

`check` 的 `modems` 应包含预期模块；`voice: true` 只表示接口存在。`check` 不会发送 Telegram 测试通知，也不保证 SIM 已注册或来电可达。`run` 会为接收短信启用已发现但未启用的模块，且不会创建或连接数据 bearer。

首次验收使用普通、无敏感内容的短信，确认各目标收到通知、日志显示全部投递完成、队列释放，且模块短信列表中的该原件已删除。若使用来电通知，再拨入测试，确认 Telegram 文字通知与号码显示。验收期间复查蜂窝主机接口仍关闭、没有地址和新增流量。长短信、断网、重启、断电和热插拔分别验证，详见[运维说明](operations.md)。
