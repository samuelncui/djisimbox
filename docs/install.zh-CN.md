# 安装到 Zero 2W

[English](install.md) · **简体中文**

使用 ARM64 Armbian / Debian 13，网络由 systemd-networkd 管理。先用 [Armbian 镜像](https://armbian.com/boards/orangepizero2w)准备 microSD 系统，配置能访问 Telegram 的 Wi-Fi，并确认能 SSH 登录。SIM 需已激活且关闭 PIN 锁。USB0 接电源，USB1 用数据线连接蜂窝模块。首次安装时，先让模块保持断开，等安装器提示后再连接。

## 1. 下载并校验

在电脑上登录 GitHub，打开 [CI 构建](https://github.com/samuelncui/djisimbox/actions/workflows/ci.yml?query=branch%3Amain)，选择 `main` 上最近一次绿色成功的运行。在 **Artifacts** 下载 `djisimbox-dev-…-linux`，解开 ZIP，取出 `linux_arm64.tar.gz` 结尾的包和 `SHA256SUMS`。

从这两个文件所在目录上传到板子。下面的 SSH 地址请替换为自己的：

```sh
ASSET=$(awk '$2 ~ /_linux_arm64.tar.gz$/ {print $2}' SHA256SUMS)
ssh orangepi@orangepizero2w.local 'mkdir -p ~/djisimbox-install'
scp "$ASSET" SHA256SUMS orangepi@orangepizero2w.local:djisimbox-install/
ssh orangepi@orangepizero2w.local
```

**以下命令均在板子上执行。** 校验并解包：

```sh
cd ~/djisimbox-install
ASSET=$(awk '$2 ~ /_linux_arm64.tar.gz$/ {print $2}' SHA256SUMS)
sha256sum --check --ignore-missing SHA256SUMS &&
  tar -xzf "$ASSET" &&
  cd "${ASSET%.tar.gz}"
```

校验必须显示 ARM64 包 `OK`；失败时重新下载。终端保持在这个解包目录。

## 2. 运行安装器

通过 [BotFather](https://core.telegram.org/bots/features#botfather) 创建 bot，保存 token，并让指定收件人在 Telegram 私聊该 bot 发送 `/start`，然后运行：

```sh
sudo ./install.sh
```

安装器会先保护 DJI 模块的 USB 网口（`2c7c:0125`），再下载依赖，安装所需软件包和服务文件。它要求使用 systemd-networkd，让 Telegram 流量走 Wi-Fi。

首次安装时，按提示完成：

1. 连接模块，按显示的 ICCID 末六位选卡，并填写通知中显示的 SIM 名称。
2. 输入 bot token；输入内容不会显示。
3. 输入指定收件人的数字聊天编号。安装器会在可用时查询 `getUpdates`，列出聊天编号供参考；通知只发给你输入的编号。

已有 Webhook 或其他程序查询该 bot 的更新时，可手动输入已知聊天编号，或换用新 bot；保留现有 Webhook。群组、频道或查询为空时见[聊天编号帮助](operations.zh-CN.md#取得-telegram-聊天编号)。

安装器会在 `/etc/djisimbox` 写入私有配置文件，检查配置和模块，启动服务并设为开机运行。再次运行会保留已有配置和 token，并更新程序及服务文件。首次引导只配置一张 SIM 和一位收件人；之后可[增加 SIM 或收件人](operations.zh-CN.md#修改收件人或增加-sim)。

## 3. 收到第一条通知

向所选 SIM 发送一条普通测试短信。指定收件人的 Telegram 收到类似通知后，才算安装完成：

```text
短信通知
SIM: 示例卡
号码: 10000
时间: 2026-01-01T12:00:00Z

测试短信
```

需要来电提醒时，再拨入该 SIM，确认收到 `来电通知`。安装器的检查不会替你发送测试通知。查看状态、日志、升级或查故障，见[日常使用](operations.zh-CN.md)。
