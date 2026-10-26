"use client";

import { useMemo, useState } from "react";
import { api, type ApiError, type UsageBucket } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { ErrorBlock, Skeleton } from "@/components/primitives";
import { useI18n, useT } from "@/lib/i18n";

type Period = "day" | "week" | "month" | "all";
const PERIODS: { value: Period; label: string }[] = [
  { value: "day", label: "usage.period.day" },
  { value: "week", label: "usage.period.week" },
  { value: "month", label: "usage.period.month" },
  { value: "all", label: "usage.period.all" },
];

export default function UsagePage() {
  const t = useT();
  const { lang } = useI18n();
  const [period, setPeriod] = useState<Period>("month");
  const [selectedProduct, setSelectedProduct] = useState<string | null>(null);
  const range = useMemo(() => {
    const until = new Date();
    // A non-zero year-one timestamp avoids the server treating Go's exact
    // zero time (year one at midnight) as an omitted range.
    if (period === "all") return { since: "0001-01-02T00:00:00.000Z", until: until.toISOString() };
    const days = period === "day" ? 1 : period === "week" ? 7 : 30;
    const since = new Date(until.getTime() - days * 24 * 60 * 60 * 1000);
    return { since: since.toISOString(), until: until.toISOString() };
  }, [period]);
  const usage = useAsync(() => api.usage(range), [range.since ?? "", range.until ?? ""]);
  const detail = useAsync(
    () => selectedProduct ? api.usage({ ...range, product_id: selectedProduct }) : Promise.resolve(null),
    [selectedProduct ?? "", range.since ?? "", range.until ?? ""],
  );

  const rows = usage.data?.usage ?? [];
  const totals = useMemo(() => rows.reduce((sum, row) => ({
    jobs: sum.jobs + row.jobs,
    cost: sum.cost + row.cost_usd,
    failures: sum.failures + row.failures,
    tokens: sum.tokens + row.tokens_in + row.tokens_out,
  }), { jobs: 0, cost: 0, failures: 0, tokens: 0 }), [rows]);
  const selected = rows.find((row) => row.product_id === selectedProduct) ?? null;

  return (
    <div className="usage-page">
      <div className="pagehead">
        <h1>{t("usage.title")}</h1>
        <span className="pagehead__sub">{t("usage.subtitle")}</span>
        <label className="usage-period">
          <span className="visually-hidden">{t("usage.period")}</span>
          <select className="input input--inline" value={period} onChange={(event) => { setPeriod(event.target.value as Period); setSelectedProduct(null); }}>
            {PERIODS.map((item) => <option key={item.value} value={item.value}>{t(item.label as "usage.period.day" | "usage.period.week" | "usage.period.month" | "usage.period.all")}</option>)}
          </select>
        </label>
      </div>

      <div className="usage-summary" aria-label={t("usage.totals")}>
        <Summary label={t("usage.totalSpend")} value={money(totals.cost, lang)} accent />
        <Summary label={t("usage.jobs")} value={integer(totals.jobs, lang)} />
        <Summary label={t("usage.tokens")} value={integer(totals.tokens, lang)} />
        <Summary label={t("usage.failures")} value={integer(totals.failures, lang)} />
      </div>

      {usage.error ? <ErrorBlock title={t("usage.loadError")} message={apiIssue(usage.error, t)} retry={() => void usage.reload()} /> : usage.loading ? (
        <div style={{ display: "grid", gap: 8 }}><Skeleton h={42} /><Skeleton h={210} /></div>
      ) : rows.length === 0 ? (
        <div className="empty panel usage-empty">
          <div className="mono tiny muted" style={{ marginBottom: 8 }}>{t("usage.empty.code", { period: period === "all" ? t("usage.allTime") : t(PERIODS.find((item) => item.value === period)!.label as "usage.period.day" | "usage.period.week" | "usage.period.month" | "usage.period.all").toUpperCase() })}</div>
          <h3>{t("usage.empty.title")}</h3>
          <p>{t("usage.empty.body")}</p>
        </div>
      ) : (
        <>
          <section className="panel usage-table-panel" aria-label={t("usage.byProduct")}>
            <div className="section__head">
              <div><h2>{t("usage.byProduct")}</h2><p className="tiny muted" style={{ margin: "3px 0 0" }}>{t("usage.selectProductHint")}</p></div>
              <span className="tiny muted" style={{ marginLeft: "auto" }}>{t("usage.productCount", { count: rows.length })}</span>
            </div>
            <div className="table-wrap">
              <table className="data usage-table">
                <thead><tr><th scope="col">{t("usage.column.product")}</th><th scope="col">{t("usage.jobs")}</th><th scope="col">{t("usage.column.tokensIn")}</th><th scope="col">{t("usage.column.tokensOut")}</th><th scope="col">{t("usage.column.cost")}</th><th scope="col">{t("usage.column.wallTime")}</th><th scope="col">{t("usage.failures")}</th></tr></thead>
                <tbody>{rows.map((row) => <ProductRow key={row.product_id} row={row} selected={selectedProduct === row.product_id} onSelect={() => setSelectedProduct((current) => current === row.product_id ? null : row.product_id)} lang={lang} />)}</tbody>
              </table>
            </div>
          </section>

          {selectedProduct ? <section className="panel usage-jobs" aria-label={t("usage.jobsForProduct")}>
            <div className="section__head">
              <div><div className="mono tiny muted">{t("usage.productDetail")}</div><h2 style={{ marginTop: 4 }}>{selected?.product_name ?? t("usage.selectedProduct")}</h2></div>
              <button className="btn btn--sm btn--ghost" type="button" onClick={() => setSelectedProduct(null)}>{t("usage.closeDetail")}</button>
            </div>
            {detail.error ? <div style={{ padding: 14 }}><ErrorBlock title={t("usage.jobUsageError")} message={apiIssue(detail.error, t)} retry={() => void detail.reload()} /></div> : detail.loading ? <div style={{ padding: 14 }}><Skeleton h={90} /></div> : detail.data?.jobs.length ? (
              <div className="table-wrap"><table className="data"><thead><tr><th>{t("usage.column.job")}</th><th>{t("usage.column.agent")}</th><th>{t("usage.column.step")}</th><th>{t("usage.column.tokens")}</th><th>{t("usage.column.cost")}</th><th>{t("usage.column.duration")}</th></tr></thead><tbody>{detail.data.jobs.map((job) => <tr key={job.id}><td className="mono tiny">{job.id.slice(0, 12)}</td><td>{job.agent}</td><td>{job.step || "—"}</td><td className="mono">{integer(job.tokens_in, lang)} / {integer(job.tokens_out, lang)}</td><td className="mono">{money(job.cost_usd, lang)}</td><td className="mono">{duration(job.wall_time, lang, t)}</td></tr>)}</tbody></table></div>
            ) : <div className="usage-detail-note"><strong>{t("usage.detailUnavailable.title")}</strong><p>{t("usage.detailUnavailable.body")}</p></div>}
          </section> : null}
        </>
      )}
    </div>
  );
}

function ProductRow({ row, selected, onSelect, lang }: { row: UsageBucket; selected: boolean; onSelect: () => void; lang: string }) {
  const t = useT();
  return <tr className={selected ? "is-selected" : ""}>
    <td><button type="button" className="usage-product-select" aria-pressed={selected} onClick={onSelect}>{row.product_name}<span aria-hidden className="usage-product-arrow">↗</span></button></td>
    <td className="mono">{integer(row.jobs, lang)}</td><td className="mono">{integer(row.tokens_in, lang)}</td><td className="mono">{integer(row.tokens_out, lang)}</td>
    <td className="mono usage-cost">{money(row.cost_usd, lang)}</td><td className="mono">{duration(row.wall_time, lang, t)}</td><td><span className={row.failures ? "usage-failures" : "muted"}>{integer(row.failures, lang)}</span></td>
  </tr>;
}

function Summary({ label, value, accent = false }: { label: string; value: string; accent?: boolean }) {
  return <div className={`usage-summary__item ${accent ? "usage-summary__item--accent" : ""}`}><span>{label}</span><strong>{value}</strong></div>;
}

function integer(value: number, lang: string): string { return new Intl.NumberFormat(lang).format(value); }
function money(value: number, lang: string): string { return new Intl.NumberFormat(lang, { style: "currency", currency: "USD", maximumFractionDigits: 4 }).format(value); }

function duration(nanoseconds: number, lang: string, t: ReturnType<typeof useT>): string {
  if (!Number.isFinite(nanoseconds) || nanoseconds <= 0) return "—";
  let seconds = Math.floor(nanoseconds / 1_000_000_000);
  const days = Math.floor(seconds / 86400); seconds %= 86400;
  const hours = Math.floor(seconds / 3600); seconds %= 3600;
  const minutes = Math.floor(seconds / 60); seconds %= 60;
  const number = (value: number) => new Intl.NumberFormat(lang).format(value);
  if (days) return `${t("usage.duration.days", { count: days })} ${t("usage.duration.hours", { count: hours })}`;
  if (hours) return `${t("usage.duration.hours", { count: hours })} ${t("usage.duration.minutes", { count: minutes })}`;
  if (minutes) return `${t("usage.duration.minutes", { count: minutes })} ${t("usage.duration.seconds", { count: seconds })}`;
  if (seconds) return t("usage.duration.seconds", { count: seconds });
  return t("usage.duration.milliseconds", { count: number(Math.max(1, Math.round(nanoseconds / 1_000_000))) });
}

function apiIssue(error: unknown, t: ReturnType<typeof useT>): string {
  if (error instanceof Error && "code" in error) { const e = error as ApiError; return e.code === "network" ? t("usage.error.network") : e.message || t("error.title"); }
  return error instanceof Error ? error.message : t("error.title");
}
