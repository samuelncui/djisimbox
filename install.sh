#!/usr/bin/env bash
set -euo pipefail
umask 077

# Top-level only: inside functions BASH_SOURCE[0] is "environment" under bash -c.
SCRIPT=${BASH_SOURCE[0]:-}

die() { printf '%s\n' "$*" >&2; exit 1; }

is_dji() {
  local device=$1
  while [[ "$device" != / && -d "$device" ]]; do
    if [[ -f "$device/idVendor" && -f "$device/idProduct" ]]; then
      [[ $(<"$device/idVendor") == 2c7c && $(<"$device/idProduct") == 0125 ]]
      return
    fi
    device=${device%/*}
  done
  return 1
}

protect_interfaces() {
  local iface device name status
  for iface in "${1:-/sys/class/net}"/*; do
    [[ -e "$iface/device" ]] || continue
    device=$(readlink -f "$iface/device")
    if ! is_dji "$device"; then continue; fi
    name=${iface##*/}
    ip link set dev "$name" down
    ip address flush dev "$name"
    networkctl reconfigure "$name"
    status=$(LC_ALL=C networkctl status "$name" --no-pager)
    if [[ "$status" != *'/etc/systemd/network/05-djisimbox-sms-only.network'* ]]; then
      ip link set dev "$name" down
      ip address flush dev "$name"
      die 'Another network rule overrides modem protection. Fix it before continuing.'
    fi
  done
}

lang=zh-CN

# say ZH EN: print the message in the selected language.
say() { if [[ $lang == en ]]; then printf '%s\n' "$2"; else printf '%s\n' "$1"; fi; }
fail() { say "$1" "$2" >&2; exit 1; }

usage() {
  printf '%s\n' \
    'Install or upgrade djisimbox with one command (Debian/Armbian, systemd-networkd):' \
    '  sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)"' \
    'Rerunning the same command upgrades and keeps the configuration.' \
    'Re-enter the setup guide later (or: sudo djisimbox setup):' \
    '  sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)" -- --setup' \
    '' \
    'Options:' \
    '  --setup            run the setup guide even if configured' \
    '  --no-setup         never run setup (first install: configure later with sudo djisimbox setup)' \
    '  --language VALUE   zh-CN or en (also the installer language)' \
    '  --token-file PATH  read the Telegram bot token from a file' \
    '  --route SIM=CHATS  SIM is an ICCID, phone number or all; CHATS are comma-separated chat IDs or -; repeatable' \
    '  --yes              non-interactive setup' \
    'Setup options imply --setup. Pin a version: DJISIMBOX_VERSION=vX.Y.Z (default: newest release, prereleases included).' \
    'Non-interactive example:' \
    '  sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)" -- --language en --token-file /root/bot-token --route all=123456789 --yes' \
    '' \
    '一条命令安装或升级 djisimbox（Debian/Armbian，systemd-networkd）：' \
    '  sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)"' \
    '重复运行同一命令即升级，配置保持不变。' \
    '重新进入配置向导：在上面命令末尾加 -- --setup，或运行 sudo djisimbox setup。' \
    '选项：--setup 强制运行向导；--no-setup 不运行向导（首次安装后用 sudo djisimbox setup 配置）；' \
    '--language、--token-file、--route、--yes 原样传给 setup（含 --setup 的效果）。' \
    '指定版本：DJISIMBOX_VERSION=vX.Y.Z（默认最新发布，含预发布）。'
}

check_language() {
  [[ $1 == zh-CN || $1 == en ]] || die '--language must be zh-CN or en. / --language 只能是 zh-CN 或 en。'
}

SETUP_MODE=auto
SETUP_ARGS=()
OPT_LANG=''
CONF_DIR=/etc/djisimbox
BIN=/usr/local/bin/djisimbox
START=1
YES=0
ASKED=0

# Validate the command line; fill SETUP_MODE, SETUP_ARGS (forwarded verbatim) and OPT_LANG.
parse_args() {
  local want=0 never=0 forwarded=0
  SETUP_MODE=auto SETUP_ARGS=() OPT_LANG='' YES=0
  while (( $# )); do
    case $1 in
      --setup) want=1 ;;
      --no-setup) never=1 ;;
      --yes) forwarded=1 YES=1; SETUP_ARGS+=("$1") ;;
      --language|--token-file|--route)
        (( $# >= 2 )) || die "Option $1 needs a value. / 选项 $1 需要一个值。"
        forwarded=1; SETUP_ARGS+=("$1" "$2")
        if [[ $1 == --language ]]; then check_language "$2"; OPT_LANG=$2; fi
        shift ;;
      --language=*|--token-file=*|--route=*)
        forwarded=1; SETUP_ARGS+=("$1")
        if [[ $1 == --language=* ]]; then check_language "${1#*=}"; OPT_LANG=${1#*=}; fi ;;
      *) die "Unknown option '$1'. See --help. / 未知选项 '$1'，见 --help。" ;;
    esac
    shift
  done
  if (( never && (want || forwarded) )); then
    die '--no-setup cannot be combined with --setup or setup options. / --no-setup 不能与 --setup 或 setup 选项同用。'
  fi
  if (( never )); then SETUP_MODE=never; elif (( want || forwarded )); then SETUP_MODE=force; fi
}

machine_arch() {
  case $(uname -m) in
    aarch64) printf '%s\n' arm64 ;;
    x86_64) printf '%s\n' amd64 ;;
    *) return 1 ;;
  esac
}

# Print the packaged binary next to the script (release or dev layout), or fail.
find_binary() {
  local dir=$1 arch
  [[ -n $dir ]] || return 1
  if [[ -f "$dir/djisimbox" && -x "$dir/djisimbox" ]]; then printf '%s\n' "$dir/djisimbox"; return; fi
  arch=$(machine_arch) || return 1
  [[ -f "$dir/bin/djisimbox-linux-$arch" && -x "$dir/bin/djisimbox-linux-$arch" ]] || return 1
  printf '%s\n' "$dir/bin/djisimbox-linux-$arch"
}

BOOT_TMP=''

# Download, verify and run the release package's installer, then clean up.
bootstrap() {
  local arch tag asset url expected actual status=0
  command -v curl >/dev/null || die 'curl is required. / 需要 curl。'
  arch=$(machine_arch) || die 'Use a 64-bit ARM64 or amd64 system. / 仅支持 64 位 ARM64 或 amd64 系统。'
  tag=${DJISIMBOX_VERSION:-}
  if [[ -z $tag ]]; then
    tag=$(curl -fsSL --retry 3 'https://api.github.com/repos/samuelncui/djisimbox/releases?per_page=1' |
      grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' |
      sed -n '1s/.*"\([^"]*\)"$/\1/p') ||
      die 'Could not find the latest release. / 无法获取最新版本。'
  fi
  [[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] ||
    die "Invalid release tag '$tag'. / 版本号无效：'$tag'。"
  printf 'Downloading djisimbox %s (%s) / 正在下载 djisimbox %s（%s）\n' "$tag" "$arch" "$tag" "$arch"

  BOOT_TMP=$(mktemp -d)
  trap 'rm -rf -- "$BOOT_TMP"' EXIT
  asset=djisimbox_linux_$arch.tar.gz
  url=https://github.com/samuelncui/djisimbox/releases/download/$tag
  curl -fsSL --retry 3 -o "$BOOT_TMP/$asset" "$url/$asset" ||
    die 'Download failed. / 下载失败。'
  curl -fsSL --retry 3 -o "$BOOT_TMP/SHA256SUMS" "$url/SHA256SUMS" ||
    die 'Download failed. / 下载失败。'

  expected=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$BOOT_TMP/SHA256SUMS")
  [[ -n $expected ]] || die 'No checksum for the package. / SHA256SUMS 中没有该安装包的校验值。'
  actual=$(sha256sum "$BOOT_TMP/$asset")
  actual=${actual%% *}
  [[ $actual == "$expected" ]] || die 'Checksum mismatch. / 校验和不匹配。'
  tar -xzf "$BOOT_TMP/$asset" -C "$BOOT_TMP" || die 'Cannot extract the package. / 无法解压安装包。'
  [[ -x "$BOOT_TMP/djisimbox/install.sh" ]] || die 'The package has no installer. / 安装包中没有 install.sh。'

  "$BOOT_TMP/djisimbox/install.sh" "$@" || status=$?
  rm -rf -- "$BOOT_TMP"
  trap - EXIT
  return "$status"
}

# Set lang from an existing config.yaml, or ask once on a first install.
choose_language() {
  local config=$1 value answer=''
  if [[ -n $OPT_LANG ]]; then
    case $OPT_LANG in en|en-*|en_*) lang=en ;; *) lang=zh-CN ;; esac
    return
  fi
  if [[ -f $config ]]; then
    value=$(sed -n 's/^language:[[:space:]]*//p' "$config" | head -n1)
    value=${value%%#*}
    value=${value//[[:space:]\"\']/}
    case $value in
      en|en-*|en_*) lang=en ;;
      *) lang=zh-CN ;;
    esac
    return
  fi
  if (( YES )); then lang=zh-CN; return; fi
  ASKED=1
  printf 'Language / 语言: 1) 中文  2) English [1]: ' >&2
  read -r answer || true
  case $answer in
    2|en|EN|English) lang=en ;;
    *) lang=zh-CN ;;
  esac
}

# Decide whether to run `djisimbox setup`; set START=0 when the service must not start yet.
setup_step() {
  local have=0 run=0
  local -a extra=()
  START=1
  if [[ -f $CONF_DIR/config.yaml ]]; then have=1; fi
  if [[ $SETUP_MODE == force || ( $SETUP_MODE == auto && $have == 0 ) ]]; then run=1; fi
  if (( ! run )); then
    if (( have )); then
      say '已保留现有配置。更换 SIM 或接收人：sudo djisimbox setup' \
        'Configuration kept. To change SIMs or recipients: sudo djisimbox setup'
    else
      START=0
    fi
    return
  fi
  if (( ASKED )); then extra=(-language "$lang"); fi
  if ! "$BIN" setup ${SETUP_ARGS[@]+"${SETUP_ARGS[@]}"} ${extra[@]+"${extra[@]}"}; then
    # Setup writes files only after it succeeds, so an existing configuration is still intact.
    if (( have )); then
      say '配置未修改，继续使用原有配置。' 'Setup did not finish; the existing configuration is kept.'
      return
    fi
    fail '配置未完成，服务未启用。请重新运行安装命令。' \
      'Setup did not finish and the service was not enabled. Rerun the install command.'
  fi
}

main() {
  local arg
  for arg in "$@"; do
    if [[ $arg == --help || $arg == -h ]]; then usage; return; fi
  done
  parse_args "$@"
  [[ $EUID == 0 ]] || die 'Run as root, for example with sudo. / 请以 root 运行，例如使用 sudo。'

  local package='' binary
  if [[ -f $SCRIPT ]]; then package=$(cd -- "$(dirname -- "$SCRIPT")" && pwd); fi
  if ! binary=$(find_binary "$package"); then
    bootstrap "$@"
    return
  fi

  [[ -f /etc/debian_version && -d /run/systemd/system ]] ||
    die 'Debian/Armbian with systemd is required. / 需要使用 systemd 的 Debian/Armbian。'
  systemctl is-active --quiet systemd-networkd ||
    die 'systemd-networkd must already manage networking. / 需要 systemd-networkd 已在管理网络。'
  choose_language "$CONF_DIR/config.yaml"
  say "正在安装 djisimbox $("$binary" version)" "Installing djisimbox $("$binary" version)"

  install -D -m 0644 "$package/deploy/networkd/05-djisimbox-sms-only.network" \
    /etc/systemd/network/05-djisimbox-sms-only.network
  networkctl reload
  protect_interfaces
  say '正在安装依赖包...' 'Installing required packages...'
  apt-get -qq update
  DEBIAN_FRONTEND=noninteractive apt-get -qq install --yes --no-install-recommends \
    ca-certificates curl jq modemmanager polkitd >/dev/null
  systemctl enable --now ModemManager
  udevadm trigger --action=change --subsystem-match=tty --sysname-match='ttyUSB*'
  udevadm settle --timeout=30

  if ! getent passwd djisimbox >/dev/null; then
    useradd --system --user-group --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin djisimbox
  fi
  install -d -m 0750 -o root -g djisimbox /etc/djisimbox
  install -m 0644 "$package/deploy/polkit/50-djisimbox.rules" /etc/polkit-1/rules.d/
  install -m 0644 "$package/deploy/systemd/djisimbox.service" /etc/systemd/system/
  install -m 0644 "$package/deploy/systemd/journald@djisimbox.conf" /etc/systemd/
  install -d -m 0755 /etc/systemd/system/systemd-journald@djisimbox.service.d
  install -m 0644 "$package/deploy/systemd/journald-djisimbox-volatile.conf" \
    /etc/systemd/system/systemd-journald@djisimbox.service.d/50-volatile.conf
  systemctl daemon-reload

  if [[ -f $CONF_DIR/config.yaml ]]; then
    "$binary" check >/dev/null ||
      fail '新版本未通过配置或调制解调器检查，已保留正在运行的服务。' \
        'The new version failed the configuration or modem check; the running service is untouched.'
  fi
  if systemctl is-active --quiet djisimbox; then
    trap 'if (( $? != 0 )); then systemctl start djisimbox || true; fi' EXIT
    systemctl stop djisimbox
  fi
  install -m 0755 "$binary" /usr/local/bin/djisimbox

  setup_step
  if (( ! START )); then
    say '安装完成，服务尚未启动。运行 sudo djisimbox setup 完成配置后服务才会启动。' \
      'Installed. The service starts after you run: sudo djisimbox setup'
    return
  fi

  runuser -u djisimbox -- /usr/local/bin/djisimbox check >/dev/null
  say '配置和调制解调器检查通过。' 'Configuration and modem check passed.'
  systemctl restart systemd-journald@djisimbox
  systemctl enable djisimbox
  systemctl restart djisimbox
  systemctl is-active --quiet djisimbox ||
    fail 'djisimbox 服务未能启动，请查看：journalctl -u djisimbox' 'djisimbox failed to start. See: journalctl -u djisimbox'
  say '安装完成。给 SIM 发一条测试短信，确认 Telegram 收到。' \
    'Installed. Send a test SMS to the SIM and check Telegram.'
  trap - EXIT
}

if [[ ${BASH_SOURCE[0]:-$0} == "$0" ]]; then main "$@"; fi
