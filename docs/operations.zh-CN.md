# 日常使用

[English](operations.md) · **简体中文**

## 修改收件人或增加 SIM

最简单的方法是引导：运行 `sudo djisimbox setup`。它会列出已连接的 SIM 和最近的聊天，让你选择收件人和语言，并重启服务。它会重新生成 `/etc/djisimbox/config.yaml`，手写的注释不会保留。

也可以手动编辑 `/etc/djisimbox/config.yaml`。每张 SIM 在 `routes` 中占一项；每位收件人在该项的 `recipients` 中占一项。下面示例把第一张卡发给两人，第二张卡发给另一人，所有编号都需替换。注释以 `#` 开头：

```yaml
version: 1
language: zh-CN   # 通知语言：zh-CN 或 en
routes:
  - iccid: "89000000000000000001"
    # name: 工作卡
    recipients:
      - bot: default
        chat_id: "123456789"
      - bot: default
        chat_id: "234567890"
  - iccid: "89000000000000000002"
    name: 备用卡
    recipients:
      - bot: default
        chat_id: "345678901"
```

使用 SIM 的完整 ICCID，不要填电话号码或 IMSI；路由只按 ICCID 匹配。`name` 可省略：省略时通知显示 SIM 内存储的本机号码；SIM 未存号码时显示 `ICCID 尾号 1234`（ICCID 末四位）。填写 `name` 时须为单行 1–80 个字符。增加 SIM 时，用 `sudo mmcli -L` 找到模块，再用 `sudo mmcli -m MODEM_ID` 找到 SIM 路径，最后用 `sudo mmcli -i SIM_ID` 读取 ICCID；模块和 SIM 的编号可能不同。多张卡同时在线需要多个模块（每个模块一张卡），两个模块同时运行已实机验证。新增收件人前，先[取得聊天编号](#取得-telegram-聊天编号)。

需要不同 bot 时，在 `/etc/djisimbox/secrets.yaml` 的 `bots` 中增加一项，例如 `family: "另一个 bot 的完整 token"`，并把相应收件人的 `bot` 改为 `family`。

保存并检查，成功后重启生效：

```sh
sudoedit /etc/djisimbox/config.yaml /etc/djisimbox/secrets.yaml
sudo -u djisimbox /usr/local/bin/djisimbox check
sudo systemctl restart djisimbox
```

修改后服务无法启动时，错误信息会指出文件名和行号。未知或拼错的字段、重复的键、多余的 YAML 文档都会被拒绝。修改只在重启后生效，没有热加载。

发送测试短信，确认指定收件人能收到。

### 取得 Telegram 聊天编号

让收件人给 bot 发 `/start`。在 Bash 终端查询更新，按提示输入 token；输入内容不会显示：

```bash
read -rsp 'Bot token: ' BOT_TOKEN; printf '\n'
printf 'url = "https://api.telegram.org/bot%s/getUpdates"\n' "$BOT_TOKEN" |
  curl --fail --silent --show-error --config -
unset BOT_TOKEN
```

从收件人消息中取 `message.chat.id`，在配置中写成字符串，如 `"123456789"`。群组中先加入 bot 并发 `/start@你的Bot用户名`；频道中将 bot 设为有发消息权限的管理员，发布测试消息，再取 `channel_post.chat.id`。群组和频道编号的负号要保留。

`getUpdates` 为空时，再发一条消息后查询。它只返回尚未被任何程序确认的近期更新（Telegram 约保留 24 小时），所以收件人很久以前才和 bot 聊过时，请对方给 bot 随便发一条新消息（无需再发 `/start`），然后重新查询。已有 Webhook 或其他程序使用该 bot 时，沿用已知聊天编号，或新建 bot；保留现有 Webhook。

不使用 bot 时，可在 Telegram 网页版（web.telegram.org）打开该聊天，地址栏 `#` 后的数字就是聊天编号；自己的编号请打开“已保存的消息”。群组的负号要保留。发送通知不需要持续查询更新，通知只发给明确配置的编号。

## 查看状态和日志

```sh
sudo systemctl status djisimbox --no-pager
sudo -u djisimbox /usr/local/bin/djisimbox status
sudo journalctl --namespace=djisimbox -u djisimbox --no-pager -n 30
```

正常投递后，`queue.Messages` 应回到 `0`。从状态和日志检查模块、配置、网络或投递错误；查看实时日志时，在 `journalctl` 命令中加 `-f`。日志重启后可能消失，排错前先保存；公开前去掉 token、SIM 编号和消息等私人信息。

## 没有收到通知

- **找不到模块**：运行 `sudo mmcli -L`。列表为空时，检查供电和 USB1 上的数据线。
- **模块没有短信**：将 `mmcli -L` 显示的编号填入 `sudo mmcli -m MODEM_ID` 和 `sudo mmcli -m MODEM_ID --messaging-list-sms`，确认 SIM 就绪并已注册网络。新发短信仍未出现时，把 SIM 放进手机测试。
- **模块有短信，Telegram 没有**：检查状态、日志、Wi-Fi 能否访问 Telegram、配置中的 ICCID、token 和聊天编号，以及 bot 的发送权限。修正后检查配置并重启服务，再发测试短信。
- **蜂窝网口保护失败**：确认 systemd-networkd 正在运行、模块 USB 身份为 `2c7c:0125`，再运行一次安装命令。其他硬件或网络管理器需另行配置网口保护。
- **短信正常，来电没有通知**：安装器不会配置语音或 VoLTE。每种 SIM、固件和运营商组合都需实际拨入测试。

## 断网、重启与换卡

断网后会重试，短信在所有收件人收到后才从模块删除；长期断网可能用满模块存储。重启可能重复转发尚未删除的短信，也可能丢失待发来电提醒；停机期间的来电也可能漏报。

长短信分成几段到达。如果有分段一直没到，这条短信就拼不起来，也无法转发；超过 24 小时后会从模块删除，腾出存储。

换 SIM 前先等 `queue.Messages` 变为 `0`，再用 `sudo mmcli -m MODEM_ID --messaging-list-sms` 确认模块没有旧短信；有旧短信时先处理，避免发给新卡的收件人。

## 升级或停用

等 `queue.Messages` 为 `0` 且没有来电后，再运行一次安装命令：

```sh
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)"
```

安装器会保留 `/etc/djisimbox/config.yaml` 和 `/etc/djisimbox/secrets.yaml`，更新程序和服务文件，检查后重启服务。最后发送测试短信确认升级成功。

停止服务并取消开机启动：

```sh
sudo systemctl disable --now djisimbox
```

保留蜂窝网口保护规则，直到你准备启用蜂窝数据。
