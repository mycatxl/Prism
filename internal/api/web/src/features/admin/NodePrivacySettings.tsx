import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ControlButton, ControlInput, ControlSwitch, ControlTextarea, WorkspaceCard } from "../../components/control-plane";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { patchSystemConfig, SYSTEM_CONFIG_QUERY_KEY } from "../systemConfig/api";
import type { NodePrivacyConfig } from "../systemConfig/types";

export function NodePrivacySettings({ config }: { config: NodePrivacyConfig }) {
  const { t } = useI18n();
  const cache = useQueryClient();
  const [own, setOwn] = useState((config.own_cidrs ?? []).join("\n"));
  const [detected, setDetected] = useState((config.detected_ips ?? []).join("\n"));
  const [manual, setManual] = useState((config.manual_quarantine ?? []).join("\n"));
  const [echoURL, setEchoURL] = useState(config.echo_url ?? "");
  const [echoEnabled, setEchoEnabled] = useState(config.echo_enabled ?? false);
  const [url, setURL] = useState(config.self_probe_url ?? "");
  const lines = (value: string) => value.split(/\s*[,\n]\s*/).map((v) => v.trim()).filter(Boolean);
  const mutation = useMutation({
    mutationFn: () => patchSystemConfig({ node_privacy: { own_cidrs: lines(own), detected_ips: lines(detected), manual_quarantine: lines(manual), self_probe_url: url.trim(), echo_url: echoURL.trim(), echo_enabled: echoEnabled } }),
    onSuccess: () => cache.invalidateQueries({ queryKey: SYSTEM_CONFIG_QUERY_KEY }),
  });
  return <WorkspaceCard title={t("节点隐私准入")}>
    <div className="space-y-4">
      <p>{t("确认明文、身份泄露或自有资产出口的节点将永久删除，任何测量开关都不能豁免。健康失败继续按原周期重试。")}</p>
      <p>{t("本机公网发现")}: <strong>{config.probe_status === "ready" ? t("成功") : t("发现未完成：资产覆盖不完整；已知自有地址仍禁止销售")}</strong> · {config.probed_at || "--"}</p>
      <p className="break-words">{t("启动发现并保留的自有地址")}: {(config.detected_ips ?? []).join(", ") || "--"}</p>
      <ControlTextarea label={t("历史发现地址，每行一个")} value={detected} onChange={(e) => setDetected(e.target.value)} rows={3} hint={t("可删除误判或已移交的历史地址；本次启动仍观察到的地址不能删除。")}/>
      <ControlTextarea label={t("自有资产 IP / CIDR，每行一个")} value={own} onChange={(e) => setOwn(e.target.value)} rows={4} hint={t("仅填运营方拥有的地址。命中后删除节点文档；修改名单不会恢复已删除节点。")}/>
      <ControlTextarea label={t("手动隔离节点哈希，每行一个")} value={manual} onChange={(e) => setManual(e.target.value)} rows={3} hint={t("删除哈希并保存可撤回手动隔离；仍命中自动规则的节点继续禁售。")}/>
      <ControlInput label={t("本机公网探测 URL")} value={url} onChange={(e) => setURL(e.target.value)} hint={t("返回纯文本公网 IP；留空使用默认服务，修改后重启生效。")}/>
      <ControlSwitch label={t("启用匿名性 echo 测量")} isSelected={echoEnabled} onChange={setEchoEnabled} hint={t("默认关闭，不宣称匿名性已验证。开启后，只有当前 echo 通过的节点可销售；关闭不会撤销已确认泄露的删除。")}/>
      <ControlInput label={t("自有 TLS echo 地址")} value={echoURL} onChange={(e) => setEchoURL(e.target.value)} hint={t("填写独立自有服务器的 HTTPS /check 地址。留空表示匿名性未测量，不会显示为通过。")}/>
      {mutation.error ? <p role="alert">{formatApiErrorMessage(mutation.error, t)}</p> : null}
      {mutation.isSuccess ? <p role="status">{t("隐私策略已保存")}</p> : null}
      <ControlButton variant="primary" isDisabled={mutation.isPending} onPress={() => mutation.mutate()}>{t("保存隐私策略")}</ControlButton>
    </div>
  </WorkspaceCard>;
}
