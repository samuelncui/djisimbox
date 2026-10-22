# 安装到 Zero 2W

[English](install.md) · **简体中文**

本指南使用 ARM64 Armbian / Debian 13，网络由 systemd-networkd 管理。先用 [Armbian 镜像](https://armbian.com/boards/orangepizero2w)准备 microSD 系统，配置 Wi-Fi 并确认能 SSH 登录。USB0 接电源，USB1 用数据线连接蜂窝模块；**先让模块保持断开，到第 2 步提示时再连接。**

## 1. 下载程序

在电脑上登录 GitHub，打开 [CI 构建](https://github.com/samuelncui/djisimbox/actions/workflows/ci.yml?query=branch%3Amain)，选择 `main` 上最近一次绿色成功的运行。在页面底部的 **Artifacts** 下载 `djisimbox-dev-…-linux`，解开 ZIP，取出 `linux_arm64.tar.gz` 结尾的包和 `SHA256SUMS`。

从这两个文件所在目录上传到板子。下面的 SSH 地址请替换为自己的：

```sh
ASSET=$(awk '$2 ~ /_linux_arm64.tar.gz$/ {print $2}' SHA256SUMS)
ssh orangepi@orangepizero2w.local 'mkdir -p ~/djisimbox-install'
scp "$ASSET" SHA256SUMS orangepi@orangepizero2w.local:djisimbox-install/
ssh orangepi@orangepizero2w.local
```

**以下命令均在板子上执行。** 安装依赖、校验并解包：

```sh
sudo apt-get update
sudo apt-get install --no-install-recommends ca-certificates curl modemmanager polkitd
cd ~/djisimbox-install
ASSET=$(awk '$2 ~ /_linux_arm64.tar.gz$/ {print $2}' SHA256SUMS)
sha256sum --check --ignore-missing SHA256SUMS &&
  tar -xzf "$ASSET" &&
  cd "${ASSET%.tar.gz}"
```

校验应显示 ARM64 包 `OK`；失败时重新下载。后续命令保持在这个解包目录执行。

## 2. 关闭蜂窝网口

先运行 `systemctl is-active systemd-networkd`，应显示 `active`。使用其他网络管理器的系统需另行配置网口保护，不能直接套用以下规则。

```sh
sudo install -m 0644 deploy/networkd/05-djisimbox-sms-only.network /etc/systemd/network/
sudo networkctl reload
```

现在连接模块，运行 `networkctl list`，找到新增的 USB 网口（如 `usb0` 或 `enx` 开头的名称）。替换下面的占位值：

```sh
MODEM_IFACE=替换为模块网口名
sudo networkctl reconfigure "$MODEM_IFACE"
networkctl status "$MODEM_IFACE" --no-pager
ip -br address show dev "$MODEM_IFACE"
ip route show default
ip -6 route show default
```

确认 `Network File` 为 `05-djisimbox-sms-only.network`、网口为 `DOWN` 且无 IP 地址，默认路由仍走 Wi-Fi。该规则适用于已验证的 DJI 模块；未匹配时先断开模块，完成保护再继续。

## 3. 取得 SIM 编号和 Telegram 收件人

启动模块管理服务，逐条执行以下查询：将 `MODEM_ID` 换成 `mmcli -L` 输出的编号，`SIM_ID` 换成 `mmcli -m` 输出里 SIM 路径末尾的编号，两者可能不同。

```sh
sudo systemctl enable --now ModemManager
sudo mmcli -L
sudo mmcli -m MODEM_ID
sudo mmcli -i SIM_ID
```

复制 SIM 的完整 **ICCID**，不要使用电话号码或 IMSI。找不到模块时见[排错](operations.zh-CN.md#没有收到通知)。

通过 [BotFather](https://core.telegram.org/bots/features#botfather) 创建 bot，保存它给出的 token。让收件人在 Telegram 私聊该 bot，发送 `/start`。在板子的 Bash 终端执行，按提示输入 token：

```bash
read -rsp 'Bot token: ' BOT_TOKEN; printf '\n'
printf 'url = "https://api.telegram.org/bot%s/getUpdates"\n' "$BOT_TOKEN" |
  curl --fail --silent --show-error --config -
unset BOT_TOKEN
```

找到刚发的消息，取 `message.chat.id`，如下面的 `123456789`，填入配置时写成字符串 `"123456789"`：

```json
{"result":[{"message":{"chat":{"id":123456789},"text":"/start"}}]}
```

结果为空时重新发 `/start` 后查询。已有 bot 查询报冲突时见[排错](operations.zh-CN.md#查不到-telegram-聊天编号)；群组和频道见[增加收件人](operations.zh-CN.md#修改收件人或增加-sim)。

## 4. 填写配置并安装服务

```sh
sudo useradd --system --user-group --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin djisimbox
sudo install -m 0755 djisimbox /usr/local/bin/djisimbox
sudo install -d -m 0750 -o root -g djisimbox /etc/djisimbox
sudo install -m 0600 -o djisimbox -g djisimbox examples/config.json /etc/djisimbox/config.json
sudo install -m 0600 -o djisimbox -g djisimbox examples/secrets.json /etc/djisimbox/secrets.json
sudoedit /etc/djisimbox/config.json /etc/djisimbox/secrets.json
```

- `config.json`：将 `iccid` 换成刚查到的 ICCID，`name` 换成通知中显示的卡名，`chat_id` 换成收件人的聊天编号。
- `secrets.json`：将 token 占位值换成 BotFather 给出的完整 token。两处 bot 名称均保留 `default`。

### 安装服务文件

保存配置后执行：

```sh
sudo install -m 0644 deploy/polkit/50-djisimbox.rules /etc/polkit-1/rules.d/
sudo install -m 0644 deploy/systemd/djisimbox.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/journald@djisimbox.conf /etc/systemd/
sudo install -d -m 0755 /etc/systemd/system/systemd-journald@djisimbox.service.d
sudo install -m 0644 deploy/systemd/journald-djisimbox-volatile.conf /etc/systemd/system/systemd-journald@djisimbox.service.d/50-volatile.conf
sudo systemctl daemon-reload
sudo -u djisimbox /usr/local/bin/djisimbox check
```

`check` 成功且 `modems` 中有你的模块后，启动服务并设为开机运行：

```sh
sudo systemctl enable --now djisimbox
sudo systemctl status djisimbox --no-pager
```

## 5. 收到第一条通知

向 SIM 发送一条普通测试短信，确认指定收件人的 Telegram 收到类似通知：

```text
短信通知
SIM: 示例卡
号码: 10000
时间: 2026-01-01T12:00:00Z

测试短信
```

需要来电提醒时，再拨入该 SIM，确认收到 `来电通知`。`check` 不会替你发送测试通知。后续改收件人、升级或查故障，见[日常使用](operations.zh-CN.md)。
