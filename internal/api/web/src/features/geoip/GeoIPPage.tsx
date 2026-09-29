import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowDownToLine, RefreshCw, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime } from "../../lib/time";
import { getRegionName } from "../nodes/regions";
import { getGeoIPStatus, lookupIP, updateGeoIPNow } from "./api";
import type { GeoIPLookupResult } from "./types";

/**
 * The address desk.
 *
 * The database state is an instrument reading — two timestamps on one baseline —
 * and the lookup result is the same strip once there is something to read. Both
 * numbers are mono so they can be compared against a log line character by
 * character. The two blocks are separate panels because they answer two separate
 * questions: what is loaded, and what does one address resolve to.
 */
export function GeoIPPage() {
  const { t } = useI18n();
  const [singleIP, setSingleIP] = useState("");
  const [singleResult, setSingleResult] = useState<GeoIPLookupResult | null>(null);
  const { toasts, showToast, dismissToast } = useToast();

  const statusQuery = useQuery({
    queryKey: ["geoip-status"],
    queryFn: getGeoIPStatus,
    refetchInterval: 60_000,
  });

  const lookupMutation = useMutation({
    mutationFn: async () => {
      const ip = singleIP.trim();
      if (!ip) {
        throw new Error(t("请输入 IP 地址"));
      }
      return lookupIP(ip);
    },
    onSuccess: (result) => {
      setSingleResult(result);
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const updateMutation = useMutation({
    mutationFn: updateGeoIPNow,
    onSuccess: async () => {
      await statusQuery.refetch();
      showToast("success", t("GeoIP 数据库更新任务已执行"));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const status = statusQuery.data;
  const hasDBTime = Boolean(status?.db_mtime);
  const hasNextSchedule = Boolean(status?.next_scheduled_update);

  const singleRegion = useMemo(() => {
    if (!singleResult || !singleResult.region) {
      return t("（空）");
    }
    const code = singleResult.region.toUpperCase();
    const name = getRegionName(code);
    if (!name) {
      return code;
    }
    return `${code} ${name}`;
  }, [singleResult, t]);

  return (
    <Page bleed>
      <PageHeader
        title={t("资源")}
        description={t("查询 IP 所在地区，并维护 GeoIP 数据库。")}
        meta={
          <>
            <PageMeta
              label={t("数据库状态")}
              value={
                <Badge tone={hasDBTime ? "signal" : "warn"} dot>
                  {hasDBTime ? t("数据库已加载") : t("数据库未加载")}
                </Badge>
              }
            />
            <PageMeta
              label={t("数据库更新时间")}
              value={hasDBTime ? formatDateTime(status?.db_mtime || "") : "-"}
            />
            <PageMeta
              label={t("下次计划更新")}
              value={hasNextSchedule ? formatDateTime(status?.next_scheduled_update || "") : "-"}
            />
          </>
        }
        actions={
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void statusQuery.refetch()}
            disabled={statusQuery.isFetching}
          >
            <RefreshCw size={14} aria-hidden className={cn(statusQuery.isFetching && "animate-spin")} />
            {t("刷新")}
          </Button>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="grid gap-3 px-4 py-3 lg:grid-cols-2 lg:px-5 lg:py-4 2xl:gap-4 2xl:px-6 2xl:py-5">
        <Panel className="min-w-0">
          <PanelHeader
            title={t("数据库状态")}
            description={t("当前加载时间与下一次计划更新时间")}
          />
          <PanelBody>
            {statusQuery.isError ? (
              <ErrorState
                message={formatApiErrorMessage(statusQuery.error, t)}
                onRetry={() => void statusQuery.refetch()}
              />
            ) : null}

            {statusQuery.isPending ? (
              <LoadingState className="py-8" />
            ) : !statusQuery.isError ? (
              <div className="space-y-3">
                <ReadoutStrip>
                  <ReadoutCell>
                    <Readout
                      label={t("数据库更新时间")}
                      value={hasDBTime ? formatDateTime(status?.db_mtime || "") : "-"}
                    />
                  </ReadoutCell>
                  <ReadoutCell>
                    <Readout
                      label={t("下次计划更新")}
                      value={hasNextSchedule ? formatDateTime(status?.next_scheduled_update || "") : "-"}
                    />
                  </ReadoutCell>
                </ReadoutStrip>

                <div className="flex flex-wrap items-center gap-2 border-t border-rule-faint pt-3">
                  <Badge tone={hasDBTime ? "signal" : "warn"} dot>
                    {hasDBTime ? t("数据库已加载") : t("数据库未加载")}
                  </Badge>
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => void updateMutation.mutateAsync()}
                    disabled={updateMutation.isPending}
                  >
                    <ArrowDownToLine
                      size={14}
                      aria-hidden
                      className={cn(updateMutation.isPending && "animate-spin")}
                    />
                    {updateMutation.isPending ? t("更新中...") : t("立即更新")}
                  </Button>
                </div>
              </div>
            ) : null}
          </PanelBody>
        </Panel>

        <Panel className="min-w-0">
          <PanelHeader title={t("单 IP 查询")} description={t("输入 IP 后点击查询。")} />
          <PanelBody>
            <div className="flex items-end gap-1.5">
              <Fieldset
                className="min-w-0 flex-1"
                label={t("输入 IP 地址例如 8.8.8.8")}
                htmlFor="geoip-single-ip"
              >
                <Input
                  id="geoip-single-ip"
                  className="readout"
                  value={singleIP}
                  onChange={(event) => setSingleIP(event.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      void lookupMutation.mutateAsync();
                    }
                  }}
                />
              </Fieldset>
              <Button
                variant="secondary"
                className="shrink-0"
                onClick={() => void lookupMutation.mutateAsync()}
                disabled={lookupMutation.isPending}
                aria-label={t("查询")}
                title={t("查询")}
              >
                <Search size={15} aria-hidden className={cn(lookupMutation.isPending && "animate-spin")} />
              </Button>
            </div>

            {singleResult ? (
              <ReadoutStrip className="mt-3">
                <ReadoutCell>
                  <Readout label="IP" value={singleResult.ip} />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout label={t("区域")} value={singleRegion} />
                </ReadoutCell>
              </ReadoutStrip>
            ) : (
              <EmptyState className="py-8" title={t("输入 IP 执行查询")} />
            )}
          </PanelBody>
        </Panel>
      </div>
    </Page>
  );
}
