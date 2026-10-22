# DJI SIM Box

[English](README.md) · **简体中文**

把 USB 蜂窝模块收到的短信和来电文字通知转发到 Telegram。每张 SIM 可指定不同收件人，也可同时发给多人。程序在 Orange Pi Zero 2W 上独立运行，通过 Wi-Fi 联网。

**[开始安装](docs/install.zh-CN.md) · [日常使用与排错](docs/operations.zh-CN.md)**

下载并校验 ARM64 安装包后，运行 `sudo ./install.sh`。安装器会引导你选择 SIM 和 Telegram 收件人，安装服务，并在升级时保留已有配置。

## 准备什么

- Zero 2W、装有 ARM64 Armbian / Debian 13 且使用 systemd-networkd 的 microSD 卡、供电和 USB 数据线。
- DJI 第一代蜂窝模块和已激活、关闭 PIN 锁的 SIM；多张卡同时在线需要多个模块。
- 能访问 Telegram 的 Wi-Fi、板子的 SSH 登录方式，以及一个 Telegram bot。

当前为早期版本，以上组合已验证短信和来电通知。其他模块、固件或运营商组合需自行测试；多模块同时运行尚未完成实机验证。

## 使用前了解

- 安装器会关闭受支持模块的蜂窝网口，让 Telegram 走 Wi-Fi。SIM 套餐和漫游仍可能产生费用，请向运营商确认。
- 来电功能只发文字提醒，不接听或传输通话音频；能否收到提醒需要实际拨入测试。
- 正式使用前，了解[断网、重启与换卡时的投递行为](docs/operations.zh-CN.md#断网重启与换卡)。

[开发资料](https://github.com/samuelncui/djisimbox/blob/main/.agents/README.md) · [MIT License](LICENSE)
