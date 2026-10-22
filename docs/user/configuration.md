# 配置 SIM 与 Telegram 收件人

配置入口为 `/etc/djisimbox/config.json`，bot token 单独保存在 `/etc/djisimbox/secrets.json`。两个文件都是严格 JSON 对象，不支持注释或未知字段，每个文件最多 1 MiB。可从包内 `examples/` 复制模板，示例身份与 token 均须替换。

## 准备 SIM 与 Telegram

在板上用 `sudo mmcli -L` 找到 modem ID，再用 `sudo mmcli -m ID` 查看 SIM 对象，最后用 `sudo mmcli -i ID` 读取完整 ICCID。两处 `ID` 分别替换为实际的 modem ID 与 SIM ID，它们不一定相同。将 ICCID 私下填入配置；分享诊断前去掉设备标识和消息内容。

通过 [BotFather](https://core.telegram.org/bots/features#botfather) 创建 bot 并取得 token，再按通知目标生成一条更新：

- 私聊：接收者给 bot 发 `/start`。
- 群组：把 bot 加入群组并允许发送消息，在群内发 `/start@你的Bot用户名`。
- 频道：把 bot 设为管理员并允许发送消息，在频道发布一条测试消息。

在 Bash 终端执行以下请求，按提示输入 token（不回显）：

```bash
read -rsp 'Bot token: ' BOT_TOKEN; printf '\n'
printf 'url = "https://api.telegram.org/bot%s/getUpdates"\n' "$BOT_TOKEN" |
  curl --fail --silent --show-error --config -
unset BOT_TOKEN
```

在返回的 `result[]` 中找到刚发的消息，读取 `message.chat.id`（私聊、群组）或 `channel_post.chat.id`（频道）。将这个数字按字符串填入 `chat_id`，保留负号；使用聊天的 `chat.id`，不要取发送者的 `from.id`。若 `result` 为空，重新发送测试消息再查询。

`getUpdates` 与 Webhook 互斥。已有其他程序管理该 bot 时，沿用其已知的目标 `chat_id`，或先用新 bot 完成上述步骤。本程序只发送通知，无需持续调用 `getUpdates`。接口详情见 [Telegram 文档](https://core.telegram.org/bots/api#getupdates)。

## 路由配置

以下值全部是示例：

```json
{
  "version": 1,
  "runtime_dir": "/run/djisimbox",
  "secrets_file": "/etc/djisimbox/secrets.json",
  "max_pending": 10000,
  "max_bytes": 16777216,
  "routes": [
    {
      "iccid": "89000000000000000001",
      "name": "示例卡",
      "recipients": [
        {"bot": "default", "chat_id": "123456789"}
      ]
    }
  ]
}
```

| 字段 | 含义与限制 |
| --- | --- |
| `version` | 必填，当前为 `1`。 |
| `runtime_dir` | 默认 `/run/djisimbox`；必须是规范的绝对路径，且位于 tmpfs。使用默认值即可匹配 systemd unit 的目录与权限。 |
| `secrets_file` | 默认 `/etc/djisimbox/secrets.json`；必须是绝对路径。 |
| `max_pending` | 默认 `10000`，范围 `1–1000000`；限制活动通知总数，包含短信、来电和未配置 SIM 的通知。 |
| `max_bytes` | 默认 `16777216`（16 MiB），范围 `1024–1073741824`；限制队列文本、元数据、分段及固定开销的逻辑预算，并非进程总内存上限。 |
| `routes[].iccid` | 18–22 位数字字符串，每张 SIM 唯一。USB 端口、手机号码与 modem ID 都不用于选收件人。 |
| `routes[].name` | 通知中显示的名称，1–80 个字符，含有效非空白内容，单行且无控制字符。该名称也会出现在运行日志中。 |
| `routes[].recipients` | 至少一项；同一 SIM 可选多个 bot/chat，重复的组合会被拒绝。 |
| `recipients[].bot` | 凭据文件中的别名，1–64 个英文字母、数字、`_` 或 `-`。 |
| `recipients[].chat_id` | 非零的有符号 64 位整数，以 JSON 字符串填写；支持负数群组 ID，配置入口使用数字 ID。 |

`routes` 为空或某张 SIM 无匹配路由时，不会选用默认收件人；可接收的通知进入隔离状态，短信原件保留。修正配置并重启后，程序重新读取原件。

每个 modem 存有短信期间应保持 SIM 固定。模块短信对象没有历史接收 ICCID，换卡前先排空已有短信；保留旧短信后物理换卡不属于已验证的路由方式。多张同时在线的 SIM 需要相应的 modem、供电和 USB 容量。

## 凭据文件

```json
{
  "bots": {
    "default": "123456789:REPLACE_WITH_YOUR_BOT_TOKEN"
  }
}
```

每个路由引用的 bot 别名必须存在。token 会做格式校验，是否有效和是否有目标发送权限仍需真实投递确认。即使某个 bot 暂未被路由引用，其别名和 token 也必须有效。

程序要求凭据为普通文件，组和其他用户均无访问权限。按安装指南使用 `0600`、属主 `djisimbox`，确保服务可读；把真实凭据保留在 `/etc/djisimbox`，避免提交到 Git 或放进发布包。

## 应用修改

```sh
sudoedit /etc/djisimbox/config.json /etc/djisimbox/secrets.json
sudo -u djisimbox /usr/local/bin/djisimbox check
sudo systemctl restart djisimbox
sudo -u djisimbox /usr/local/bin/djisimbox status
```

配置在进程启动时加载。重启会清空投递进度；未删除短信可能再次发给已成功的目标，待发来电通知可能丢失。运行中服务仍使用原配置，`check` 读取的是磁盘上的新配置。
