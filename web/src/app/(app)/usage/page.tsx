"use client";

import { useMemo, useState } from "react";
import { api, type ApiError, type UsageBucket } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { ErrorBlock, Skeleton } from "@/components/primitives";

type Period = "day" | "week" | "month" | "all";
const PERIODS: { value: Period; label: string }[] = [
  { value: "day", label: "Last day" },
  { value: "week", label: "Last week" },
  { value: "month", label: "Last 30 days" },
  { value: "all", label: "Everything" },
];

export default function UsagePage() {
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
        <h1>Usage</h1>
        <span className="pagehead__sub">Spend and work attributed to products.</span>
        <label className="usage-period">
          <span className="visually-hidden">Usage period</span>
          <select className="input input--inline" value={period} onChange={(event) => { setPeriod(event.target.value as Period); setSelectedProduct(null); }}>
            {PERIODS.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
          </select>
        </label>
      </div>

      <div className="usage-summary" aria-label="Usage totals">
        <Summary label="Total spend" value={money(totals.cost)} accent />
        <Summary label="Jobs" value={integer(totals.jobs)} />
        <Summary label="Tokens" value={integer(totals.tokens)} />
        <Summary label="Failures" value={integer(totals.failures)} />
      </div>

      {usage.error ? <ErrorBlock title="Usage could not load." message={apiIssue(usage.error)} retry={() => void usage.reload()} /> : usage.loading ? (
        <div style={{ display: "grid", gap: 8 }}><Skeleton h={42} /><Skeleton h={210} /></div>
      ) : rows.length === 0 ? (
        <div className="empty panel usage-empty">
          <div className="mono tiny muted" style={{ marginBottom: 8 }}>USAGE / {period === "all" ? "ALL TIME" : PERIODS.find((item) => item.value === period)?.label.toUpperCase()}</div>
          <h3>No usage in this period</h3>
          <p>Once jobs run against a product, their tokens, cost and runtime will show up here.</p>
        </div>
      ) : (
        <>
          <section className="panel usage-table-panel" aria-label="Usage by product">
            <div className="section__head">
              <div><h2>By product</h2><p className="tiny muted" style={{ margin: "3px 0 0" }}>Select a product to inspect its job usage.</p></div>
              <span className="tiny muted" style={{ marginLeft: "auto" }}>{rows.length} {rows.length === 1 ? "product" : "products"}</span>
            </div>
            <div className="table-wrap">
              <table className="data usage-table">
                <thead><tr><th scope="col">Product</th><th scope="col">Jobs</th><th scope="col">Tokens in</th><th scope="col">Tokens out</th><th scope="col">Cost</th><th scope="col">Wall time</th><th scope="col">Failures</th></tr></thead>
                <tbody>{rows.map((row) => <ProductRow key={row.product_id} row={row} selected={selectedProduct === row.product_id} onSelect={() => setSelectedProduct((current) => current === row.product_id ? null : row.product_id)} />)}</tbody>
              </table>
            </div>
          </section>

          {selectedProduct ? <section className="panel usage-jobs" aria-label="Jobs for selected product">
            <div className="section__head">
              <div><div className="mono tiny muted">PRODUCT / USAGE DETAIL</div><h2 style={{ marginTop: 4 }}>{selected?.product_name ?? "Selected product"}</h2></div>
              <button className="btn btn--sm btn--ghost" type="button" onClick={() => setSelectedProduct(null)}>Close detail</button>
            </div>
            {detail.error ? <div style={{ padding: 14 }}><ErrorBlock title="Job usage could not load." message={apiIssue(detail.error)} retry={() => void detail.reload()} /></div> : detail.loading ? <div style={{ padding: 14 }}><Skeleton h={90} /></div> : detail.data?.jobs.length ? (
              <div className="table-wrap"><table className="data"><thead><tr><th>Job</th><th>Agent</th><th>Step</th><th>Tokens</th><th>Cost</th><th>Duration</th></tr></thead><tbody>{detail.data.jobs.map((job) => <tr key={job.id}><td className="mono tiny">{job.id.slice(0, 12)}</td><td>{job.agent}</td><td>{job.step || "—"}</td><td className="mono">{integer(job.tokens_in)} / {integer(job.tokens_out)}</td><td className="mono">{money(job.cost_usd)}</td><td className="mono">{duration(job.wall_time)}</td></tr>)}</tbody></table></div>
            ) : <div className="usage-detail-note"><strong>Product-level job rows aren’t available from this response.</strong><p>The usage endpoint returns job detail for an item ID, but product totals don’t include item IDs. The product totals above are complete; no job rows were returned.</p></div>}
          </section> : null}
        </>
      )}
    </div>
  );
}

function ProductRow({ row, selected, onSelect }: { row: UsageBucket; selected: boolean; onSelect: () => void }) {
  return <tr className={selected ? "is-selected" : ""}>
    <td><button type="button" className="usage-product-select" aria-pressed={selected} onClick={onSelect}>{row.product_name}<span aria-hidden className="usage-product-arrow">↗</span></button></td>
    <td className="mono">{integer(row.jobs)}</td><td className="mono">{integer(row.tokens_in)}</td><td className="mono">{integer(row.tokens_out)}</td>
    <td className="mono usage-cost">{money(row.cost_usd)}</td><td className="mono">{duration(row.wall_time)}</td><td><span className={row.failures ? "usage-failures" : "muted"}>{integer(row.failures)}</span></td>
  </tr>;
}

function Summary({ label, value, accent = false }: { label: string; value: string; accent?: boolean }) {
  return <div className={`usage-summary__item ${accent ? "usage-summary__item--accent" : ""}`}><span>{label}</span><strong>{value}</strong></div>;
}

function integer(value: number): string { return new Intl.NumberFormat().format(value); }
function money(value: number): string { return new Intl.NumberFormat(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 4 }).format(value); }

function duration(nanoseconds: number): string {
  if (!Number.isFinite(nanoseconds) || nanoseconds <= 0) return "—";
  let seconds = Math.floor(nanoseconds / 1_000_000_000);
  const days = Math.floor(seconds / 86400); seconds %= 86400;
  const hours = Math.floor(seconds / 3600); seconds %= 3600;
  const minutes = Math.floor(seconds / 60); seconds %= 60;
  if (days) return `${days}d ${hours}h`;
  if (hours) return `${hours}h ${minutes}m`;
  if (minutes) return `${minutes}m ${seconds}s`;
  if (seconds) return `${seconds}s`;
  return `${Math.max(1, Math.round(nanoseconds / 1_000_000))}ms`;
}

function apiIssue(error: unknown): string {
  if (error instanceof Error && "code" in error) { const e = error as ApiError; return `${e.code}: ${e.message}`; }
  return error instanceof Error ? error.message : "Something went wrong.";
}
