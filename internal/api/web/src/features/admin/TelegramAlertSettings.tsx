import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ControlButton, ControlInput, ControlSwitch, StatusMark, ToastContainer, WorkspaceCard } from "../../components/control-plane";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { patchSystemConfig, sendTestOperationalAlert, SYSTEM_CONFIG_QUERY_KEY } from "../systemConfig/api";
import type { OperationalAlertSettings, RuntimeConfigPatch } from "../systemConfig/types";

export function TelegramAlertSettings({ config }: { config: OperationalAlertSettings }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [enabled, setEnabled] = useState(config.alert_telegram_enabled ?? false);
  const [chatID, setChatID] = useState(config.alert_telegram_chat_id ?? "");
  const [token, setToken] = useState("");
  const [removeToken, setRemoveToken] = useState(false);
  const [diskPercent, setDiskPercent] = useState(String(config.alert_disk_free_percent ?? 10));
  const [cooldown, setCooldown] = useState(String(config.alert_cooldown_minutes ?? 15));
  const [error, setError] = useState("");
  const tokenSet = config.alert_telegram_bot_token_set === true;
  const dirty = enabled !== (config.alert_telegram_enabled ?? false) || chatID !== (config.alert_telegram_chat_id ?? "") || Boolean(token) || removeToken || Number(diskPercent) !== (config.alert_disk_free_percent ?? 10) || Number(cooldown) !== (config.alert_cooldown_minutes ?? 15);
  const incomplete = enabled && (!chatID.trim() || (!token && (!tokenSet || removeToken)));
  const invalidThreshold = !/^\d+$/.test(diskPercent) || Number(diskPercent) < 1 || Number(diskPercent) > 50 || !/^\d+$/.test(cooldown) || Number(cooldown) < 1 || Number(cooldown) > 1440;
  const save = useMutation({
    mutationFn: (patch: RuntimeConfigPatch) => patchSystemConfig(patch),
    onSuccess: async () => { setToken(""); setRemoveToken(false); setError(""); showToast("success", t("告警设置已保存")); await queryClient.invalidateQueries({ queryKey: SYSTEM_CONFIG_QUERY_KEY }); },
    onError: (failure) => { const message = formatApiErrorMessage(failure, t); setError(message); showToast("error", message); },
  });
  const test = useMutation({
    mutationFn: sendTestOperationalAlert,
    onSuccess: () => { setError(""); showToast("success", t("测试通知已发送")); },
    onError: (failure) => { const message = formatApiErrorMessage(failure, t); setError(message); showToast("error", message); },
  });
  const pending = save.isPending || test.isPending;
  const submit = () => {
    const patch: RuntimeConfigPatch = { alert_telegram_enabled: enabled, alert_telegram_chat_id: chatID.trim(), alert_disk_free_percent: Number(diskPercent), alert_cooldown_minutes: Number(cooldown) };
    if (token || removeToken) patch.alert_telegram_bot_token = removeToken ? "" : token.trim();
    save.mutate(patch);
  };

  return <WorkspaceCard title={t("外部运维告警")} description={t("通过 Telegram 通知磁盘压力、数据库锁争用、用量刷新失败及节点池故障。")} meta={<StatusMark tone={config.alert_telegram_enabled ? "live" : "neutral"} label={t(config.alert_telegram_enabled ? "通知已启用" : "通知已关闭")} />}>
    <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    <div className="workspace-stack">
      <ControlSwitch label={t("启用 Telegram 通知")} isSelected={enabled} onChange={setEnabled} isDisabled={pending} />
      <div className="workspace-form-grid">
        <ControlInput label="Bot token" type="password" autoComplete="new-password" value={token} disabled={pending} placeholder={t(tokenSet && !removeToken ? "已配置；留空保留当前 token" : "输入 Telegram Bot token")} hint={t("已保存的 token 不会回显。更换时输入新值。")} onChange={(event) => { setToken(event.target.value); setRemoveToken(false); }} />
        <ControlInput label={t("Telegram 聊天 ID")} value={chatID} disabled={pending} placeholder="-1001234567890" hint={t("支持个人或群组聊天 ID，以及 @channel 用户名。")} onChange={(event) => setChatID(event.target.value)} />
        <ControlInput label={t("磁盘可用空间告警阈值（%）")} type="number" min="1" max="50" step="1" value={diskPercent} disabled={pending} onChange={(event) => setDiskPercent(event.target.value)} />
        <ControlInput label={t("相同故障通知间隔（分钟）")} type="number" min="1" max="1440" step="1" value={cooldown} disabled={pending} onChange={(event) => setCooldown(event.target.value)} />
      </div>
      {tokenSet ? <div className="workspace-inline-value"><StatusMark tone={removeToken ? "warn" : "neutral"} label={t(removeToken ? "保存后将移除 token" : "Bot token 已配置")} /><ControlButton variant="ghost" size="sm" isDisabled={pending} onPress={() => { setRemoveToken(!removeToken); setToken(""); }}>{t(removeToken ? "保留当前 token" : "移除 Bot token")}</ControlButton></div> : null}
      {incomplete ? <p className="workspace-form-error" role="alert">{t("启用通知前请配置 Bot token 和聊天 ID。")}</p> : null}
      {invalidThreshold ? <p className="workspace-form-error" role="alert">{t("磁盘阈值须为 1–50，通知间隔须为 1–1440 的整数。")}</p> : null}
      {error ? <p className="workspace-form-error" role="alert">{error}</p> : null}
      <div className="workspace-list-actions"><ControlButton variant="secondary" size="sm" onPress={() => test.mutate()} isDisabled={pending || dirty || !config.alert_telegram_enabled || !tokenSet} loading={test.isPending}>{t("发送测试通知")}</ControlButton><ControlButton variant="primary" size="sm" onPress={submit} isDisabled={pending || !dirty || incomplete || invalidThreshold} loading={save.isPending}>{t("保存告警设置")}</ControlButton></div>
      <p className="workspace-muted">{t("保存后可发送测试通知；持续故障会按通知间隔再次提醒。")}</p>
    </div>
  </WorkspaceCard>;
}
