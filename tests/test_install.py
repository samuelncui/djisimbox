"""Installer boundaries using synthetic sysfs, ModemManager and Telegram data."""

import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


INSTALLER = Path(__file__).resolve().parents[1] / "install.sh"
class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="djisimbox-install-test-")
        self.addCleanup(self.temporary.cleanup)
        self.fixture = Path(self.temporary.name)
        self.config = self.fixture / "config"
        self.config.mkdir()

    def bash(self, commands, input="", **env):
        return subprocess.run(
            ["bash", "-c", 'source "$1"\n' + commands, "bash", str(INSTALLER)],
            input=input, text=True, capture_output=True,
            env=dict(os.environ, FIXTURE=str(self.fixture), **env), timeout=10,
        )

    def device(self, name, vendor="2c7c", product="0125"):
        device = self.fixture / "devices" / name
        device.mkdir(parents=True)
        (device / "idVendor").write_text(vendor + "\n")
        (device / "idProduct").write_text(product + "\n")
        interface = self.fixture / "net" / name
        interface.mkdir(parents=True)
        child = device / "1-1:1.0" / "net"
        child.mkdir(parents=True)
        (interface / "device").symlink_to(child)
        return device

    def test_protects_only_matching_usb_modem(self):
        self.device("cell0")
        self.device("wifi0", vendor="1234")
        result = self.bash('''
ip() { printf 'ip %s\\n' "$*" >>"$FIXTURE/trace"; }
networkctl() {
  printf 'networkctl %s\\n' "$*" >>"$FIXTURE/trace"
  if [[ $1 == status ]]; then
    printf '%s\\n' 'Network File: /etc/systemd/network/05-djisimbox-sms-only.network'
  fi
}
protect_interfaces "$FIXTURE/net"
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.fixture / "trace").read_text().splitlines(), [
            "ip link set dev cell0 down", "ip address flush dev cell0",
            "networkctl reconfigure cell0", "networkctl status cell0 --no-pager",
        ])

    def test_overriding_rule_stops_and_leaves_modem_disabled(self):
        self.device("cell0")
        result = self.bash('''
ip() { printf 'ip %s\\n' "$*" >>"$FIXTURE/trace"; }
networkctl() { printf '%s\\n' 'Network File: /etc/systemd/network/00-other.network'; }
protect_interfaces "$FIXTURE/net"
touch "$FIXTURE/continued"
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("overrides modem protection", result.stderr)
        self.assertFalse((self.fixture / "continued").exists())
        self.assertEqual((self.fixture / "trace").read_text().splitlines()[-2:], [
            "ip link set dev cell0 down", "ip address flush dev cell0",
        ])

    # ---- bootstrap ----

    def package(self, name="djisimbox_linux_arm64.tar.gz", script=None, sums=None):
        """Create a fake release asset plus SHA256SUMS under FIXTURE/release."""
        release = self.fixture / "release"
        release.mkdir(exist_ok=True)
        script = script or '#!/bin/sh\nprintf "%s\\n" "$@" >"$FIXTURE/inner-args"\nexit "${INNER_STATUS:-0}"\n'
        data = script.encode()
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
            info = tarfile.TarInfo("djisimbox/install.sh")
            info.size, info.mode = len(data), 0o755
            archive.addfile(info, io.BytesIO(data))
        (release / name).write_bytes(buffer.getvalue())
        digest = hashlib.sha256(buffer.getvalue()).hexdigest()
        (release / "SHA256SUMS").write_text(sums if sums is not None else f"{digest}  {name}\n")

    BOOTSTRAP = r"""
mkdir -p "$FIXTURE/tmp"; export TMPDIR="$FIXTURE/tmp"
uname() { printf '%s\n' "${FAKE_ARCH:-aarch64}"; }
curl() {
  printf 'curl %s\n' "$*" >>"$FIXTURE/curl"
  local out='' url=${@: -1}
  [[ $1 == -fsSL && $2 == --retry && $3 == 3 ]] || return 2
  [[ $4 == -o ]] && out=$5
  case $url in
    https://api.github.com/repos/samuelncui/djisimbox/releases?per_page=1)
      printf '%s\n' '[{"url":"x","tag_name": "v0.1.0-rc.1","name":"rc"}]' ;;
    https://github.com/samuelncui/djisimbox/releases/download/*/*)
      cp "$FIXTURE/release/${url##*/}" "$out" ;;
    *) return 22 ;;
  esac
}
bootstrap "$@"
status=$?
exit $status
"""

    def run_bootstrap(self, *args, **env):
        return subprocess.run(
            ["bash", "-c", 'source "$1"; shift\n' + self.BOOTSTRAP, "bash", str(INSTALLER), *args],
            text=True, capture_output=True, timeout=10,
            env=dict(os.environ, FIXTURE=str(self.fixture), **env))

    def curl_log(self):
        return (self.fixture / "curl").read_text().splitlines()

    def leftover_tmp(self):
        tmp = self.fixture / "tmp"
        return list(tmp.iterdir()) if tmp.exists() else []

    def test_bootstrap_uses_newest_release_and_forwards_arguments(self):
        self.package()
        result = self.run_bootstrap("--flag", "two words", INNER_STATUS="0")
        self.assertEqual(result.returncode, 0, result.stderr)
        base = "https://github.com/samuelncui/djisimbox/releases/download/v0.1.0-rc.1"
        log = self.curl_log()
        self.assertIn("curl -fsSL --retry 3 https://api.github.com/repos/samuelncui/djisimbox/releases?per_page=1", log)
        self.assertTrue(any(line.endswith(f"{base}/djisimbox_linux_arm64.tar.gz") for line in log), log)
        self.assertTrue(any(line.endswith(f"{base}/SHA256SUMS") for line in log), log)
        self.assertIn("Downloading djisimbox v0.1.0-rc.1 (arm64) / 正在下载 djisimbox v0.1.0-rc.1（arm64）", result.stdout)
        self.assertEqual((self.fixture / "inner-args").read_text().splitlines(), ["--flag", "two words"])
        self.assertEqual(self.leftover_tmp(), [])

    def test_bootstrap_amd64_asset(self):
        self.package("djisimbox_linux_amd64.tar.gz")
        result = self.run_bootstrap(FAKE_ARCH="x86_64", DJISIMBOX_VERSION="v1.2.3")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("djisimbox_linux_amd64.tar.gz", "\n".join(self.curl_log()))

    def test_version_variable_skips_api(self):
        self.package()
        result = self.run_bootstrap(DJISIMBOX_VERSION="v9.8.7")
        self.assertEqual(result.returncode, 0, result.stderr)
        log = "\n".join(self.curl_log())
        self.assertNotIn("api.github.com", log)
        self.assertIn("/download/v9.8.7/djisimbox_linux_arm64.tar.gz", log)

    def test_invalid_tag_is_rejected_before_any_download(self):
        self.package()
        for tag in ["latest", "v1.2", "v1.2.3/../x", "1.2.3", "v1.2.3 ", "v1.2.3;id"]:
            with self.subTest(tag=tag):
                result = self.run_bootstrap(DJISIMBOX_VERSION=tag)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Invalid release tag", result.stderr)
                self.assertFalse((self.fixture / "curl").exists())
                self.assertFalse((self.fixture / "inner-args").exists())

    def test_checksum_mismatch_or_missing_line_aborts_and_cleans_up(self):
        for label, sums in [("mismatch", "0" * 64 + "  djisimbox_linux_arm64.tar.gz\n"),
                            ("missing", "0" * 64 + "  djisimbox_linux_amd64.tar.gz\n"),
                            ("empty", "")]:
            with self.subTest(label=label):
                self.package(sums=sums)
                result = self.run_bootstrap(DJISIMBOX_VERSION="v1.0.0")
                self.assertNotEqual(result.returncode, 0)
                self.assertRegex(result.stderr, "Checksum mismatch|No checksum")
                self.assertFalse((self.fixture / "inner-args").exists())
                self.assertEqual(self.leftover_tmp(), [])

    def test_inner_status_is_propagated_and_temp_removed(self):
        self.package()
        result = self.run_bootstrap(DJISIMBOX_VERSION="v1.0.0", INNER_STATUS="7")
        self.assertEqual(result.returncode, 7)
        self.assertEqual(self.leftover_tmp(), [])

    def test_unknown_architecture_is_rejected(self):
        result = self.run_bootstrap(FAKE_ARCH="armv7l")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ARM64", result.stderr)
        self.assertFalse((self.fixture / "curl").exists())

    # ---- language ----

    def language(self, config=None, input=""):
        path = self.fixture / "config.yaml"
        if config is not None:
            path.write_text(config)
        result = self.bash('choose_language "$FIXTURE/config.yaml"\nprintf "%s" "$lang"\n', input)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result

    def test_language_is_read_from_existing_config(self):
        self.assertEqual(self.language('version: 1\nlanguage: en\n').stdout, "en")
        self.assertEqual(self.language('language: "en" # comment\n').stdout, "en")
        self.assertEqual(self.language('language: zh-CN\n').stdout, "zh-CN")
        self.assertEqual(self.language('language: fr\n').stdout, "zh-CN")

    def test_language_defaults_to_chinese_when_line_absent_and_never_asks(self):
        result = self.language('version: 1\n', input="2\n")
        self.assertEqual(result.stdout, "zh-CN")
        self.assertNotIn("Language", result.stderr)

    def test_language_is_asked_without_config(self):
        for answer, expected in [("2\n", "en"), ("1\n", "zh-CN"), ("\n", "zh-CN"), ("", "zh-CN")]:
            with self.subTest(answer=answer):
                result = self.language(input=answer)
                self.assertEqual(result.stdout, expected)
                self.assertIn("Language / 语言: 1) 中文  2) English [1]:", result.stderr)

    # ---- entry point ----

    # ---- options ----

    def parse(self, *args):
        quoted = " ".join("'" + a + "'" for a in args)
        return self.bash(f"""parse_args {quoted}
printf '%s\\n' "$SETUP_MODE" "$OPT_LANG"; printf '[%s]\\n' "${{SETUP_ARGS[@]}}"
""")

    def test_parse_accepts_forms_and_keeps_route_order(self):
        result = self.parse("--language=en", "--route", "all=1,2", "--token-file", "/t", "--route=+86138=-", "--yes")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), [
            "force", "en", "[--language=en]", "[--route]", "[all=1,2]", "[--token-file]", "[/t]",
            "[--route=+86138=-]", "[--yes]"])

    def test_parse_modes(self):
        self.assertEqual(self.parse().stdout.splitlines()[0], "auto")
        self.assertEqual(self.parse("--setup").stdout.splitlines()[0], "force")
        self.assertEqual(self.parse("--no-setup").stdout.splitlines()[0], "never")
        self.assertEqual(self.parse("--yes").stdout.splitlines()[0], "force")

    def test_parse_rejects_unknown_conflicts_and_missing_values(self):
        for args in [("--bogus",), ("--setup", "--no-setup"), ("--no-setup", "--yes"),
                     ("--route",), ("--language",), ("setup",)]:
            with self.subTest(args=args):
                self.assertNotEqual(self.parse(*args).returncode, 0)

    def test_unknown_option_is_rejected_before_any_download(self):
        marker = self.fixture / "called"
        fakebin = self.fixture / "bin"
        fakebin.mkdir()
        (fakebin / "curl").write_text(f'#!/bin/sh\necho curl >>"{marker}"\n')
        (fakebin / "curl").chmod(0o755)
        result = subprocess.run(["bash", str(INSTALLER), "--bogus"], text=True, capture_output=True,
                                timeout=5, env=dict(os.environ, PATH=f"{fakebin}:{os.environ['PATH']}"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Unknown option", result.stderr)
        self.assertFalse(marker.exists())

    def test_bash_c_with_double_dash_zero_runs_main(self):
        result = subprocess.run(["bash", "-c", INSTALLER.read_text(), "--", "--setup", "--no-setup"],
                                text=True, capture_output=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--no-setup cannot be combined", result.stderr)

    def test_bootstrap_forwards_options_unchanged(self):
        self.package()
        result = self.run_bootstrap("--setup", "--route=all=-", "--token-file", "/a b", DJISIMBOX_VERSION="v1.0.0")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.fixture / "inner-args").read_text().splitlines(),
                         ["--setup", "--route=all=-", "--token-file", "/a b"])

    def setup_step(self, args, config_exists, lang="zh-CN", setup_status=0, asked=0):
        if config_exists:
            (self.config / "config.yaml").write_text("language: en\n")
        (self.fixture / "djisimbox").write_text(
            '#!/bin/sh\nprintf "%s\\n" "$@" >"$FIXTURE/setup-args"\n'
            f'if [ {setup_status} = 0 ]; then echo x >"$FIXTURE/config/config.yaml"; echo x >"$FIXTURE/config/secrets.yaml"; fi\n'
            f'exit {setup_status}\n')
        (self.fixture / "djisimbox").chmod(0o755)
        quoted = " ".join(f"'{a}'" for a in args)
        return self.bash(f"""CONF_DIR="$FIXTURE/config" BIN="$FIXTURE/djisimbox" lang={lang} ASKED={asked}
chown() {{ :; }}
parse_args {quoted}
setup_step
printf 'START=%s\\n' "$START"
""")

    def setup_args(self):
        path = self.fixture / "setup-args"
        return path.read_text().splitlines() if path.exists() else None

    def test_setup_decision_matrix(self):
        cases = [
            ([], False, True, "1"), ([], True, False, "1"),
            (["--setup"], False, True, "1"), (["--setup"], True, True, "1"),
            (["--yes"], True, True, "1"), (["--route", "all=-"], True, True, "1"),
            (["--no-setup"], False, False, "0"), (["--no-setup"], True, False, "1"),
        ]
        for args, exists, ran, start in cases:
            with self.subTest(args=args, config=exists):
                for path in self.fixture.glob("config/*"):
                    path.unlink()
                (self.fixture / "setup-args").unlink(missing_ok=True)
                result = self.setup_step(args, exists)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f"START={start}", result.stdout)
                self.assertEqual(self.setup_args() is not None, ran)

    def test_setup_receives_language_once(self):
        self.setup_step([], False, lang="en", asked=1)
        self.assertEqual(self.setup_args(), ["setup", "-language", "en"])
        (self.config / "config.yaml").unlink()
        self.setup_step(["--language", "en", "--yes"], False)
        self.assertEqual(self.setup_args(), ["setup", "--language", "en", "--yes"])

    def test_failed_setup_stops_before_start(self):
        result = self.setup_step([], False, setup_status=3)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("START=", result.stdout)

    def test_failed_setup_on_existing_config_keeps_it_and_starts(self):
        result = self.setup_step(["--setup"], True, setup_status=3)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("START=1", result.stdout)
        self.assertEqual((self.config / "config.yaml").read_text(), "language: en\n")

    def test_language_option_skips_prompt(self):
        result = self.bash('parse_args --language en\nchoose_language "$FIXTURE/none.yaml"\nprintf "%s" "$lang"\n', input="1\n")
        self.assertEqual(result.stdout, "en")
        self.assertNotIn("Language", result.stderr)

    def test_language_option_is_validated(self):
        self.assertEqual(self.parse("--language=zh-CN").returncode, 0)
        for value in ["fr", "EN", ""]:
            with self.subTest(value=value):
                self.assertNotEqual(self.parse(f"--language={value}").returncode, 0)

    def test_yes_never_prompts_and_existing_config_passes_no_language(self):
        result = self.bash('parse_args --yes\nchoose_language "$FIXTURE/none.yaml"\nprintf "%s %s" "$lang" "$ASKED"\n', input="2\n")
        self.assertEqual(result.stdout, "zh-CN 0")
        self.assertNotIn("Language", result.stderr)
        self.setup_step(["--setup"], True)
        self.assertEqual(self.setup_args(), ["setup"])

    def test_cwd_djisimbox_is_never_executed_under_bash_c(self):
        marker = self.fixture / "executed"
        fakebin = self.fixture / "bin"
        fakebin.mkdir()
        (fakebin / "curl").write_text(f'#!/bin/sh\necho curl >>"{self.fixture}/curl-called"\nexit 22\n')
        (fakebin / "curl").chmod(0o755)
        for kind in ("file", "dir"):
            with self.subTest(kind=kind):
                work = self.fixture / kind
                work.mkdir()
                if kind == "file":
                    (work / "djisimbox").write_text(f'#!/bin/sh\ntouch "{marker}"\n')
                    (work / "djisimbox").chmod(0o755)
                else:
                    (work / "djisimbox").mkdir()
                subprocess.run(["bash", "-c", INSTALLER.read_text(), "--"], cwd=work, text=True,
                               capture_output=True, timeout=10, env=dict(
                                   os.environ, DJISIMBOX_VERSION="v1.0.0", PATH=f"{fakebin}:{os.environ['PATH']}"))
                self.assertFalse(marker.exists())
                if os.geteuid() == 0:
                    self.assertTrue((self.fixture / "curl-called").exists())

    def test_bash_c_runs_main_but_sourcing_does_not(self):
        script = INSTALLER.read_text()
        result = subprocess.run(["bash", "-c", script, "bash", "--help"],
                                text=True, capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("DJISIMBOX_VERSION=vX.Y.Z", result.stdout)
        result = self.bash('echo sourced-only\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "sourced-only\n")

    def test_help_has_no_installation_side_effects(self):
        marker = self.fixture / "called"
        fakebin = self.fixture / "bin"
        fakebin.mkdir()
        for name in ["curl", "apt-get", "systemctl", "install", "mktemp", "useradd"]:
            (fakebin / name).write_text(f'#!/bin/sh\necho {name} >>"{marker}"\n')
            (fakebin / name).chmod(0o755)
        result = subprocess.run([str(INSTALLER), "--help"], text=True, capture_output=True, timeout=5,
                                env=dict(os.environ, PATH=f"{fakebin}:{os.environ['PATH']}"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("sudo bash -c", result.stdout)
        self.assertIn("raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh", result.stdout)
        self.assertIn("DJISIMBOX_VERSION=vX.Y.Z", result.stdout)
        self.assertIn("sudo djisimbox setup", result.stdout)
        self.assertNotIn("BOT_TOKEN", result.stdout)
        for option in ["-- --setup", "--no-setup", "--token-file", "--route", "--yes"]:
            self.assertIn(option, result.stdout)
        self.assertFalse(marker.exists())


if __name__ == "__main__":
    unittest.main()
