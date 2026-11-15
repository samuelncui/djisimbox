# Install on Zero 2W

**English** · [简体中文](install.zh-CN.md)

## Prepare

- A microSD card with ARM64 [Armbian](https://armbian.com/boards/orangepizero2w) (Debian 13), Wi-Fi that can reach Telegram, and SSH access to the board.
- An activated SIM with its PIN lock disabled. USB0 is for power; USB1 connects to the DJI cellular modem with a data cable.
- A bot created with [BotFather](https://core.telegram.org/bots/features#botfather); keep its token. Ask the recipient to send the bot any message in Telegram, for example `/start`.

## Install

Connect to the board over SSH and run this one command:

```sh
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)"
```

It downloads the latest version, first turns off the DJI modem's USB network interfaces (no SIM data is used), installs the service, then starts the guide:

1. Choose a language (中文 or English); notifications use it too.
2. Enter the bot token; your input is hidden.
3. For each detected SIM, shown by its phone number, choose recipients: pick numbers from the list of chats that recently messaged the bot, or type chat IDs.

You're done when it prints `Installed.` Send the SIM an SMS and the recipient's Telegram receives:

```text
SMS
SIM: +8613800000000
From: 10000
Time: 2026-01-01T12:00:00Z

Test SMS
```

## Later

- **Upgrade**: run the same command again; your configuration is kept.
- **Add a SIM, change recipients or language**: run `sudo djisimbox setup` to go through the guide again, or add `-- --setup` to the install command to upgrade and enter the guide.
- **Skip the guide (unattended)**: store the token in a file only root can read, give the language and recipients as options, and add `--yes` so nothing is asked:

  ```sh
  sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)" -- \
    --language en --token-file /root/bot-token --route all=123456789 --yes
  ```

  `--route` can repeat, as `SIM=chat IDs`: the SIM is an ICCID, a phone number or `all` (every SIM connected when the guide runs); separate chat IDs with commas, or use `-` to stop forwarding that SIM. Without `--yes`, each option you give only skips its own question.
- If a recipient is missing from the list, or you want a group or channel, see [chat ID help](operations.md#find-a-telegram-chat-id); for status, logs and troubleshooting, see [everyday use](operations.md).
