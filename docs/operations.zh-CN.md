# 日常使用

[English](operations.md) · **简体中文**

## 修改收件人或增加 SIM

编辑 `/etc/djisimbox/config.json`。每张 SIM 在 `routes` 中占一项；每位收件人在该项的 `recipients` 中占一项。下面示例把第一张卡发给两人，第二张卡发给另一人，所有编号都需替换：

```json
{
  "version": 1,
  "routes": [
    {
      "iccid": "89000000000000000001",
      "name": "工作卡",
      "recipients": [
        {"bot": "default", "chat_id": "123456789"},
        {"bot": "default", "chat_id": "234567890"}
      ]
    },
    {
      "iccid": "89000000000000000002",
      "name": "备用卡",
      "recipients": [
        {"bot": "default", "chat_id": "345678901"}
      ]
    }
  ]
}
```

使用 SIM 的完整 ICCID，不要填电话号码或 IMSI。增加 SIM 时，用 `sudo mmcli -L` 找到模块，再用 `sudo mmcli -m MODEM_ID` 找到 SIM 路径，最后用 `sudo mmcli -i SIM_ID` 读取 ICCID；模块和 SIM 的编号可能不同。多张卡同时在线需要多个模块，目前尚未完成实机验证。新增收件人前，先[取得聊天编号](#取得-telegram-聊天编号)。

需要不同 bot 时，在 `/etc/djisimbox/secrets.json` 的 `bots` 中增加一项，例如 `"family": "另一个 bot 的完整 token"`，并把相应收件人的 `bot` 改为 `family`。

保存并检查，成功后重启生效：

```sh
sudoedit /etc/djisimbox/config.json /etc/djisimbox/secrets.json
sudo -u djisimbox /usr/local/bin/djisimbox check
sudo systemctl restart djisimbox
```

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

`getUpdates` 为空时，再发一条消息后查询。已有 Webhook 或其他程序使用该 bot 时，沿用已知聊天编号，或新建 bot；保留现有 Webhook。发送通知不需要持续查询更新，通知只发给明确配置的编号。

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
- **蜂窝网口保护失败**：确认 systemd-networkd 正在运行、模块 USB 身份为 `2c7c:0125`，再从解包目录运行 `sudo ./install.sh`。其他硬件或网络管理器需另行配置网口保护。
- **短信正常，来电没有通知**：安装器不会配置语音或 VoLTE。每种 SIM、固件和运营商组合都需实际拨入测试。

## 断网、重启与换卡

断网后会重试，短信在所有收件人收到后才从模块删除；长期断网可能用满模块存储。重启可能重复转发尚未删除的短信，也可能丢失待发来电提醒；停机期间的来电也可能漏报。

换 SIM 前先等 `queue.Messages` 变为 `0`，再用 `sudo mmcli -m MODEM_ID --messaging-list-sms` 确认模块没有旧短信；有旧短信时先处理，避免发给新卡的收件人。

## 升级或停用

按[下载和校验步骤](install.zh-CN.md#1-下载并校验)取得新包，在板子上使用新的空目录。等 `queue.Messages` 为 `0` 且没有来电后，从新包的解包目录执行：

```sh
sudo ./install.sh
```

安装器会保留 `/etc/djisimbox/config.json` 和 `/etc/djisimbox/secrets.json`，更新程序和服务文件，检查后重启服务。最后发送测试短信确认升级成功。

停止服务并取消开机启动：

```sh
sudo systemctl disable --now djisimbox
```

保留蜂窝网口保护规则，直到你准备启用蜂窝数据。
