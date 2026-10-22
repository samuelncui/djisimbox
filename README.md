# DJI SIM Box

在 Orange Pi Zero 2W 上接收 USB 蜂窝模块的短信和来电事件，按 SIM 转发到指定的 Telegram bot/chat。程序用 Go 编写，通过 ModemManager 的 D-Bus 接口接收消息；设备通过 Wi-Fi 访问 Telegram，独立运行，无需 NAS 或其他服务器。

当前为初期实现。已在一组 Zero 2W 与 DJI 第一代蜂窝模块上验证真实短信和来电文字通知；其他模块、SIM、运营商组合需要单独验证。兼容范围见[硬件验证](https://github.com/samuelncui/djisimbox/blob/main/docs/development/hardware.md)。

## 能做什么

- 每张 SIM 按完整 ICCID 显式配置一个或多个 bot/chat；未配置的 SIM 保留消息，等待配置。
- 短信原件保留在模块上，所有目标的全部文本分段均获 Telegram 成功响应后才删除；网络故障会重试。
- 来电响铃或呼叫等待时发送文字通知，包含可取得的号码；程序只观察来电，不接听、拨号、挂断或处理音频。
- 队列、重试状态和服务日志放在 RAM，运行目录使用 tmpfs；短信和来电通知采用相同的中文标题与元数据格式。

重启会清空队列与投递记录，尚未删除的短信会重新读取，可能重复发送。来电没有持久原件，停机、过短的响铃、队列满或重启可能丢失通知；持续响铃期间重启也可能重复通知。可等待的断网时长受模块短信存储容量限制。

主机上的蜂窝网口必须保持关闭且没有地址，Telegram 使用独立网络。该规则无法保证模块内部没有 IMS 等通信，也不能承诺没有漫游费用；使用前确认自己的套餐和运营商政策。

## 开始使用

用户可下载预编译的 Linux ARM64 程序，无需在 Zero 2W 安装 Go。目前尚未发布首个版本；首个 Release 发布后可按安装指南下载，也可先使用成功的 Actions 构建产物或从源码构建。

1. 按[安装指南](docs/user/install.md)准备系统、保护蜂窝网口并安装服务。
2. 按[配置说明](docs/user/configuration.md)填写 ICCID、bot token 与 chat ID。
3. 按[日常运维](docs/user/operations.md)检查投递、查看日志和处理故障。

安装完成后常用命令：

```sh
sudo -u djisimbox /usr/local/bin/djisimbox check
sudo -u djisimbox /usr/local/bin/djisimbox status
sudo journalctl --namespace=djisimbox -u djisimbox --no-pager -n 30
```

`check` 校验配置、运行目录和当前硬件接口；`status` 读取运行中服务的私有 Unix socket。两者都不能代替真实短信与来电的验收。

## 开发资料

[架构、投递语义与源码构建](https://github.com/samuelncui/djisimbox/blob/main/docs/development/architecture.md)、[硬件验证](https://github.com/samuelncui/djisimbox/blob/main/docs/development/hardware.md)、[Zero 2W 启动与 USB 约束](https://github.com/samuelncui/djisimbox/blob/main/docs/development/zero2w-usb.md)、[QDC507 VoLTE 研究](https://github.com/samuelncui/djisimbox/blob/main/docs/development/volte-research.md)、[构建与发布流程](https://github.com/samuelncui/djisimbox/blob/main/docs/development/releasing.md)。

项目采用 [MIT License](LICENSE)。
