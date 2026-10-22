# DJI SIM Box

**English** · [简体中文](README.zh-CN.md)

Forward SMS messages and incoming call text alerts from a USB cellular modem to Telegram. Each SIM can have its own recipients, including multiple people. The program runs on a standalone Orange Pi Zero 2W connected over Wi-Fi.

**[Install](docs/install.md) · [Use and troubleshoot](docs/operations.md)**

## What you need

- A Zero 2W, a microSD card running Armbian / Debian 13, a power supply, and a USB data cable.
- A first-generation DJI cellular modem and an activated SIM with its PIN lock disabled. Using several SIMs at once requires several modems.
- Wi-Fi with access to Telegram, SSH access to the board, and a Telegram bot.

This is an early version. SMS and call alerts have been verified with the setup above. Other modems, firmware, and carriers need testing; running multiple modems together has not been tested on hardware.

## Before you use it

- Disable the board's cellular network interface during installation so Telegram uses Wi-Fi. SIM plans and roaming may still incur charges; check with your carrier.
- SMS messages are deleted from the modem after delivery to all recipients. A restart may resend messages that have not yet been deleted, and call alerts may be missed during downtime.
- Call support sends text alerts only. It does not answer calls or carry call audio; make a test call to confirm alerts work.

[Development and hardware research](https://github.com/samuelncui/djisimbox/blob/main/.agents/README.md) · [MIT License](LICENSE)
