# QDC507 VoLTE 研究

本文区分被测结果、公开线索与尚未证明的推断，供新硬件诊断使用。程序的来电功能只观察 ModemManager 属性；它不会自动配置 IMS、选择 MBN 或更新固件。

## 被测结果与解释

在 `QDC507GLEFM21_01.001.01.005` 上，原 `OpenMkt-Commercial-CU` 配置出现过短信可收而呼入不可达。仅设置 IMS override 后，查询仍为 `1,0`；这只说明该次修改没有完成来电启用，未证明运营商拒绝的具体原因。

后续选择模块已有的 `ROW_Generic_3GPP`，保持 IMS override 为 `1`，模块重启后查询变为 `1,1`，可见 PDP 配置出现 `ims` APN。`AutoSel=0`，主机侧自动连接设置读回 `0`。真实来电通知和短信均完成 Telegram 投递，见[硬件验证](hardware.md)。该结果只适用于被测组合，ROW 并非所有 SIM/网络的通用配置。

[Quectel IMS Application Note §1.2–1.3](https://quectel.com/content/uploads/2024/02/Quectel_EC2xEG9xEG2x-GEM05_Series_IMS_Application_Note_V1.0-3.pdf#page=9)说明 MBN 自动选择与 SIM 身份有关，切换 profile 会应用 NV/EFS 默认值，可能覆盖原有 AT 设置。因此，漫游网络名称不足以决定应选哪个 MBN，选回原 profile 也不等于完整恢复所有 NV 状态。

诊断中遇到的 `CGCONTRDP` 可选 IMS 字段缺失、APN 名称没有 `ims`、`QCPDPIMSCFGE` 配置标志为零，都不能排除固件内部或共享 context 的 IMS。某命令返回 `ERROR` 时，其设置保持未知。[标准 AT 手册](https://quectel.com/content/uploads/2024/05/Quectel_EC2xEG9xEG2x-GEM05_Series_AT_Commands_Manual_V2.0.pdf)和 [QCFG 手册](https://quectel.com/content/uploads/2024/02/Quectel_EC2xEG2xEG9xEM05_Series_QCFG_AT_Commands_Manual_V1.0.pdf)用于解释标准系列字段；定制 QDC507 的支持与语义仍以实际响应及厂商确认作为边界。

## 可参考的公开材料

| 材料 | 能支持的结论 | 边界 |
| --- | --- | --- |
| [QDC507 `.01.009` 的 MBN 清单](https://github.com/glasses666/dji4g-qdc507-research/blob/1218ac361e65917cd41b82cebe5ea89e72549462/modem/backup_redacted/at/mbn_profiles.txt) | 另一设备记录了 `CU-VoLTE` profile。 | 不提供本项目固件的可用迁移或来电验收。 |
| [QDC507 CTExcel 测试](https://github.com/ywang3129-cell/ctexcel-sms-dji/blob/main/DJI-QDC507-CTEXCEL.md) | 作者记录 ROW 与 IMS 设置后的真实短信。 | SIM、固件和网络不同；短信结果不能证明语音可用。 |
| [Quectel 论坛的 CU IMS 问题](https://forums.quectel.com/t/eg25-gc-volte-ims-not-registering-request-latest-firmware-and-updated-cu-mbn/59337) | 标准 EG25 也存在相似的旧固件/配置问题线索。 | 公开回复没有给出本项目定制固件的确定修复与验收。 |
| [QDC507 固件恢复材料](https://github.com/CwithW/qdc507-firmware-recovery/tree/8b1dd5fa0d5bdfc2a465d844ebfc1fa2801ffc9b) | 存在目标为 `.02.004` 的公开分区镜像与 manifest。 | [验证记录](https://github.com/CwithW/qdc507-firmware-recovery/blob/8b1dd5fa0d5bdfc2a465d844ebfc1fa2801ffc9b/docs/VALIDATION.md)没有硬件恢复验收。 |

既往对公开 `system.bin` 的离线检查匹配其 manifest 校验值，并可读取 UBI/UBIFS 目录；未发现独立的 `CU-VoLTE`/`mcfg_sw.mbn` 文件。这个结果不排除打包在其他数据中的配置，也不证明可刷写、兼容或可回退。公开镜像中的固件、专利及其他第三方材料应按其各自许可处理，本项目不随发行包分发这些镜像。

## 进一步实验的验收边界

先记录模块/SIM 身份、固件、当前 MBN、AutoSel、IMS、完整 PDP/APN 状态与可工作的短信基线，保留主机蜂窝网口保护。独占 AT 访问会中断 ModemManager 接收，应在队列排空且没有活动呼叫时安排维护，并先准备服务恢复。

被测实验只使用模块已存在的 profile，未导入 MBN 或刷写固件。新实验应按本机基线制定修改与恢复步骤；profile 激活可能影响其他设置，恢复后需比较完整状态。

成功必须包含真实呼入、被动通知入队和 Telegram 成功响应，另行复验短信与原件释放。IMS 状态、APN 或 profile 名称只提供中间证据。模块重新枚举后还需确认主机网口仍受保护；主机零流量不包含模块内部 IMS，也不构成免漫游费用保证。

导入新 MBN 需要可兼容的 MPSS/MCFG 与实际文件；更新固件还需要确认硬件存储结构、备份范围和已验证的恢复路径。目前的公开材料不足以完成这些证明。
