package setup

import "github.com/samuelncui/djisimbox/internal/config"

// texts holds every user-facing string for one language.
type texts struct {
	badConfig           string
	startNew            string
	keepBot             string
	keepBotUnknown      string
	askToken            string
	badToken            string
	tokenRejected       string
	tooManyTokens       string
	botReady            string
	noSIM               string
	noSIMQuit           string
	simsFound           string
	numberUnknown       string
	simLabel            string
	chatsHeader         string
	chatsEmpty          string
	chatsFailed         string
	currentRecips       string
	hintKeep            string
	hintSkip            string
	badRecipients       string
	keptRoutes          string
	summaryTitle        string
	summaryLanguage     string
	summaryBot          string
	notForwarded        string
	askSave             string
	noChanges           string
	saved               string
	serviceStarted      string
	serviceNotInstalled string
	serviceNotChanged   string
	cannotReach         string
	retryPrompt         string
	waitingSIM          string
	restartFailed       string
	askAgain            string
	unconfigured        string
	configHeader        string
}

var texts_en = texts{
	badConfig:           "The existing configuration cannot be used: %v",
	startNew:            "Start a new configuration instead? Existing bot tokens are kept. [y/N]",
	keepBot:             "Keep the current bot @%s? [Y/n]",
	keepBotUnknown:      "Keep the current bot? [Y/n]",
	askToken:            "Paste the bot token from @BotFather (typing is hidden):",
	badToken:            "That does not look like a bot token. Try again.",
	tokenRejected:       "Telegram did not accept that token (%v). Try again.",
	tooManyTokens:       "no valid bot token after 3 attempts",
	botReady:            "Bot verified: @%s",
	noSIM:               "No SIM found. Connect the modem with its SIM, then press Enter to retry (q to quit):",
	noSIMQuit:           "no SIM found",
	simsFound:           "SIM cards found:",
	numberUnknown:       "number unknown",
	simLabel:            "%s (ICCID ending %s)",
	chatsHeader:         "Recent chats with the bot:",
	chatsEmpty:          "No chats yet. Ask each recipient to send any message to the bot, then press Enter to refresh, or type a chat ID.",
	chatsFailed:         "Chats cannot be listed (a webhook or another program may be using the bot). Type chat IDs instead.",
	currentRecips:       "Current recipients: %s",
	hintKeep:            "Recipients: list numbers or chat IDs, separated by commas or spaces. Enter keeps the current ones, - stops forwarding.",
	hintSkip:            "Recipients: list numbers or chat IDs, separated by commas or spaces. Enter skips this SIM, - does the same.",
	badRecipients:       "Not understood: %q. Use a number from the list, or a chat ID (4 or more digits; negative for groups).",
	keptRoutes:          "%d configured SIM(s) not connected now were kept unchanged.",
	summaryTitle:        "Summary:",
	summaryLanguage:     "Language: %s",
	summaryBot:          "Bot: %s",
	notForwarded:        "not forwarded",
	askSave:             "Save? [Y/n]",
	noChanges:           "No changes saved",
	saved:               "Saved %s and %s.",
	serviceStarted:      "The djisimbox service is enabled and running with the new settings.",
	restartFailed:       "Settings saved, but the service could not be started",
	serviceNotInstalled: "The djisimbox service is not installed yet; it will use these settings once installed.",
	serviceNotChanged:   "%s is not the service's configuration file, so the service was not changed.",
	cannotReach:         "Cannot reach Telegram. Check the network connection.",
	retryPrompt:         "Try again? [Y/n]",
	waitingSIM:          "Waiting for the SIM (up to 60 seconds)...",
	askAgain:            "Please answer y or n.",
	unconfigured:        "%s has no recipients yet; set them with --route or run the guide interactively.",
	configHeader: `# DJI SIM Box configuration, written by "sudo djisimbox setup".
# Run the guide again to change it, or edit by hand and restart: sudo systemctl restart djisimbox
# To label a SIM in notifications, add 'name: Work phone' under its iccid; otherwise its phone number is shown.
`,
}

var texts_zh = texts{
	badConfig:           "现有配置无法使用：%v",
	startNew:            "是否改为新建配置？已有的机器人令牌会保留。[y/N]",
	keepBot:             "继续使用当前机器人 @%s？[Y/n]",
	keepBotUnknown:      "继续使用当前机器人？[Y/n]",
	askToken:            "请粘贴 @BotFather 给出的机器人令牌（输入不会显示）：",
	badToken:            "这不像机器人令牌，请重试。",
	tokenRejected:       "Telegram 未接受该令牌（%v），请重试。",
	tooManyTokens:       "3 次尝试后仍无有效的机器人令牌",
	botReady:            "机器人已验证：@%s",
	noSIM:               "未发现 SIM 卡。请连接带 SIM 卡的调制解调器后按回车重试（输入 q 退出）：",
	noSIMQuit:           "未发现 SIM 卡",
	simsFound:           "发现的 SIM 卡：",
	numberUnknown:       "号码未知",
	simLabel:            "%s（ICCID 尾号 %s）",
	chatsHeader:         "最近与机器人对话的聊天：",
	chatsEmpty:          "暂无聊天。请让每位接收人给机器人发任意一条消息，然后按回车刷新，或直接输入聊天 ID。",
	chatsFailed:         "无法列出聊天（可能有 webhook 或其他程序正在使用该机器人），请直接输入聊天 ID。",
	currentRecips:       "当前接收人：%s",
	hintKeep:            "接收人：输入列表序号或聊天 ID，用逗号或空格分隔。回车保持不变，- 停止转发。",
	hintSkip:            "接收人：输入列表序号或聊天 ID，用逗号或空格分隔。回车跳过此 SIM 卡，- 效果相同。",
	badRecipients:       "无法识别：%q。请输入列表中的序号，或聊天 ID（至少 4 位数字，群组为负数）。",
	keptRoutes:          "另有 %d 张已配置但当前未连接的 SIM 卡，配置保持不变。",
	summaryTitle:        "配置摘要：",
	summaryLanguage:     "语言：%s",
	summaryBot:          "机器人：%s",
	notForwarded:        "不转发",
	askSave:             "保存？[Y/n]",
	noChanges:           "未保存任何更改",
	saved:               "已保存 %s 和 %s。",
	serviceStarted:      "djisimbox 服务已启用并使用新设置运行。",
	restartFailed:       "设置已保存，但服务启动失败",
	serviceNotInstalled: "djisimbox 服务尚未安装；安装后将使用这些设置。",
	serviceNotChanged:   "%s 不是服务使用的配置文件，因此未改动服务。",
	cannotReach:         "无法连接 Telegram，请检查网络。",
	retryPrompt:         "是否重试？[Y/n]",
	waitingSIM:          "正在等待 SIM 卡（最多 60 秒）……",
	askAgain:            "请回答 y 或 n。",
	unconfigured:        "%s 尚未设置接收人；请使用 --route 设置，或交互运行该向导。",
	configHeader: `# DJI SIM Box 配置，由 "sudo djisimbox setup" 生成。
# 可再次运行该向导修改，或手工编辑后重启：sudo systemctl restart djisimbox
# 如需在通知中给 SIM 卡加标签，在其 iccid 下添加 'name: 工作手机'；否则显示 SIM 卡的手机号码。
`,
}

func textsFor(language string) *texts {
	if language == config.LanguageEnglish {
		return &texts_en
	}
	return &texts_zh
}
