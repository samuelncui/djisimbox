# Zero 2W 启动与 USB 约束

本项目最终采用板卡兼容的 Armbian microSD 系统，通过 Wi-Fi/SSH 管理。USB 串口与蜂窝模块是运行接口，电脑能否通过主板 USB 获得管理会话是另一项配置。

## 可复用的结论

- [Orange Pi 官方手册](http://www.orangepi.org/orangepiwiki/index.php/Orange_Pi_Zero_2W)区分支持 device 模式的 USB0 与 host USB1，并说明板载 SPI 可包含出厂测试系统；看到 LED 闪烁不能证明 microSD 系统或 SSH 已就绪。
- [厂商设备树](https://github.com/orangepi-xunlong/linux-orangepi/blob/orange-pi-6.1-sun50iw9/arch/arm64/boot/dts/allwinner/sun50i-h618-orangepi-zero2w.dts)将 USB0 配为 peripheral，并使用 heartbeat LED。实际镜像的设备树和用户态配置可能不同。
- device 模式只确定控制器角色。Linux 还需配置并启用 [USB gadget 功能](https://docs.kernel.org/usb/gadget_configfs.html)，主机才能枚举串口或网络设备。
- 既往连接中曾短暂出现 Allwinner FEL，但未完成可用的协议会话。VID/PID、短暂枚举或 LED 状态不能证明能读写板卡；SPI 内容和稳定 FEL 恢复仍未验证。

## 建立可用管理入口

准备已确认兼容 Zero 2W 的 microSD 镜像，按镜像说明配置 Wi-Fi/SSH。需要本地诊断时，使用 mini HDMI 与键盘，或按厂商引脚说明使用 **3.3 V USB-to-TTL UART**。先取得启动日志与 shell，再检查系统、存储及 USB gadget；正常使用本项目无需 FEL 或修改 SPI。

在 Linux 开发主机上观察 USB 连接可用：

```sh
lsusb
lsusb -t
sudo udevadm monitor --kernel --subsystem-match=usb
```

这些命令用于枚举和观察，不建立管理功能。主板可通过 Wi-Fi 管理后，按[安装指南](../docs/install.zh-CN.md)先保护蜂窝主机网口，再连接模块。已验证的运行组合见[硬件验证](hardware.md)。
