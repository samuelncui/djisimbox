# 安装到 Zero 2W

[English](install.md) · **简体中文**

## 准备

- microSD 卡装好 ARM64 [Armbian](https://armbian.com/boards/orangepizero2w)（Debian 13），配置能访问 Telegram 的 Wi-Fi，并能 SSH 登录板子。
- SIM 已激活且关闭 PIN 锁。USB0 接电源，USB1 用数据线连接 DJI 蜂窝模块。
- 用 [BotFather](https://core.telegram.org/bots/features#botfather) 创建一个 bot，记下 token；让收件人在 Telegram 里给这个 bot 发一条任意消息（比如 `/start`）。

## 安装

SSH 登录板子，运行这一条命令：

```sh
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)"
```

它会下载最新版本，先关闭 DJI 模块的 USB 网口（不走 SIM 流量），装好服务，然后进入引导：

1. 选择语言（中文或 English），通知文字也用这种语言。
2. 输入 bot token（输入内容不会显示）。
3. 列出识别到的每张 SIM（显示电话号码），给每张卡选收件人：从“最近给 bot 发过消息的聊天”列表里选编号，或直接输入聊天编号。

看到 `安装完成` 就好了。给 SIM 发一条短信，收件人的 Telegram 会收到：

```text
短信通知
SIM: +8613800000000
号码: 10000
时间: 2026-01-01T12:00:00Z

测试短信
```

## 以后

- **升级**：再运行一次上面那条命令，配置会保留。
- **加卡、改收件人或语言**：运行 `sudo djisimbox setup` 重新走一遍引导；也可以在安装命令后加 `-- --setup`，升级的同时进入引导。
- **跳过引导（无人值守）**：把 token 存进一个只有 root 能读的文件，用选项直接给出语言和收件人，加 `--yes` 后不再提问：

  ```sh
  sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)" -- \
    --language zh-CN --token-file /root/bot-token --route all=123456789 --yes
  ```

  `--route` 可以重复，写法是 `卡=聊天编号`：卡可以是 ICCID、电话号码或 `all`（运行引导时已连接的所有卡），多个聊天编号用逗号分隔，`-` 表示停止转发这张卡。只给部分选项、不加 `--yes` 时，只跳过对应的问题。
- 列表里没有收件人、要发到群组或频道，见[聊天编号帮助](operations.zh-CN.md#取得-telegram-聊天编号)；查看状态、日志或排查问题，见[日常使用](operations.zh-CN.md)。
