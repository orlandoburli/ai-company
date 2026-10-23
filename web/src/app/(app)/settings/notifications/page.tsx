"use client";

import { FormEvent, useMemo, useState } from "react";
import {
  api,
  type ApiError,
  type NotificationChannel,
  type NotificationChannelInput,
  type NotificationEvent,
} from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { SettingsNav } from "@/components/settings-nav";
import { CopyButton, Dialog, EmptyState, ErrorBlock, Field, Skeleton, Toasts } from "@/components/primitives";

const EVENTS: { value: NotificationEvent; label: string; detail: string }[] = [
  { value: "checkpoint_pending", label: "Checkpoint pending", detail: "A workflow is waiting for a human decision." },
  { value: "run_failed", label: "Run failed", detail: "A workflow run ended in failure." },
  { value: "run_completed", label: "Run completed", detail: "A workflow run finished successfully." },
];

const PLACEHOLDERS = [
  { value: "{{event.kind}}", detail: "The event name, such as run_failed." },
  { value: "{{run.id}}", detail: "The workflow run ID." },
  { value: "{{item.id}}", detail: "The item ID that the run belongs to." },
  { value: "{{title}}", detail: "A short notification title." },
  { value: "{{body}}", detail: "The notification details." },
  { value: "{{link}}", detail: "A link to the related item or run." },
];

export default function NotificationChannelsPage() {
  const { me } = useSession();
  const { toasts, push } = useToasts();
  const channelsQ = useAsync(() => api.listNotificationChannels().then((r) => r.channels), []);
  const [editing, setEditing] = useState<NotificationChannel | "new" | null>(null);
  const isManager = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const channels = useMemo(() => [...(channelsQ.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [channelsQ.data]);

  async function remove(channel: NotificationChannel) {
    if (!window.confirm(`Delete the “${channel.name}” command channel?`)) return;
    try {
      await api.deleteNotificationChannel(channel.id);
      await channelsQ.reload();
      push("info", `“${channel.name}” was deleted.`);
    } catch (error) { push("error", apiIssue(error)); }
  }

  return <div className="notification-settings-page">
    <div className="pagehead">
      <h1>Notification channels</h1>
      <span className="pagehead__sub">Send workflow events to a command on the server.</span>
    </div>
    <SettingsNav />

    <div className="notification-safety">
      <span className="notification-safety__mark" aria-hidden>!</span>
      <div><strong>Commands run on the server.</strong><p>A command channel is a command this server runs on your machine when an event happens. Only an owner or admin may configure one.</p></div>
    </div>

    <section className="section">
      <div className="section__head">
        <div><h2>Command channels</h2><p className="tiny muted" style={{ margin: "3px 0 0" }}>Choose which events should run each command.</p></div>
        <button type="button" className="btn btn--sm btn--primary" style={{ marginLeft: "auto" }} onClick={() => setEditing("new")}>New channel</button>
      </div>
      {!isManager ? <p className="notification-role-note">Your role is <strong>{me?.tenant.role ?? "read only"}</strong>. Only an owner or admin can save changes; if the server rejects an action, its error code will be shown.</p> : null}
      <div className="section__body">
        {channelsQ.error ? <ErrorBlock title="Command channels could not load." message={apiIssue(channelsQ.error)} retry={() => void channelsQ.reload()} /> : !channelsQ.data ? <Skeleton h={110} /> : channels.length === 0 ? <EmptyState title="No command channels yet" body="Add a channel to run a command when a workflow checkpoint is waiting, a run fails, or a run completes." /> : (
          <div className="table-wrap">
            <table className="data notification-channels-table">
              <thead><tr><th scope="col">Name</th><th scope="col">Events</th><th scope="col">Enabled</th><th scope="col">Command template</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>{channels.map((channel) => <tr key={channel.id}>
                <td className="notification-channel-name">{channel.name}</td>
                <td><div className="notification-event-list">{channel.events.map((event) => <span key={event} className="badge-outline mono">{event}</span>)}</div></td>
                <td><span className={`notification-enabled ${channel.enabled ? "notification-enabled--on" : ""}`}><i aria-hidden />{channel.enabled ? "Enabled" : "Off"}</span></td>
                <td><code className="notification-command" title={channel.command_template}>{channel.command_template}</code></td>
                <td><div className="stack" style={{ gap: 6 }}><button type="button" className="btn btn--sm" onClick={() => setEditing(channel)}>Edit</button><button type="button" className="btn btn--sm btn--ghost btn--danger" onClick={() => void remove(channel)}>Delete</button></div></td>
              </tr>)}</tbody>
            </table>
          </div>
        )}
      </div>
    </section>

    {editing ? <ChannelEditor channel={editing === "new" ? null : editing} onClose={() => setEditing(null)} onSave={async (body) => {
      if (editing === "new") await api.createNotificationChannel(body);
      else await api.updateNotificationChannel(editing.id, body);
      setEditing(null);
      await channelsQ.reload();
      push("info", "Command channel saved.");
    }} onError={(error) => push("error", apiIssue(error))} /> : null}
    <Toasts toasts={toasts} />
  </div>;
}

function ChannelEditor({ channel, onClose, onSave, onError }: {
  channel: NotificationChannel | null;
  onClose: () => void;
  onSave: (body: NotificationChannelInput) => Promise<void>;
  onError: (error: unknown) => void;
}) {
  const [name, setName] = useState(channel?.name ?? "");
  const [command, setCommand] = useState(channel?.command_template ?? "");
  const [events, setEvents] = useState<NotificationEvent[]>(channel?.events ?? []);
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim() || !command.trim() || events.length === 0) {
      setFormError("Add a name, a command template, and at least one event.");
      return;
    }
    setBusy(true);
    setFormError("");
    try { await onSave({ name: name.trim(), command_template: command.trim(), events, enabled }); }
    catch (error) { const message = apiIssue(error); setFormError(message); onError(error); }
    finally { setBusy(false); }
  }

  return <Dialog title={channel ? "Edit command channel" : "New command channel"} description="A command this server runs on your machine when an event happens. Only an owner or admin may configure one." onClose={onClose} width={760}>
    <div className="notification-admin-note"><strong>Who can configure this?</strong> An owner or admin. The server checks your role when you save; its error code will be shown if it refuses.</div>
    <form className="channel-editor" onSubmit={(event) => void submit(event)}>
      <Field label="Channel name"><input className="input" autoFocus value={name} onChange={(event) => setName(event.target.value)} placeholder="Desktop alert" /></Field>
      <Field label="Command template" hint="Use a command available to the server. Placeholders are replaced with event data.">
        <textarea className="input channel-command-input mono" rows={3} value={command} onChange={(event) => setCommand(event.target.value)} placeholder={'notify-send "{{title}}" "{{body}}"'} />
      </Field>
      <fieldset className="channel-events"><legend>Watch these events</legend><div className="channel-events__list">{EVENTS.map((item) => <label key={item.value} className="channel-event-option"><input type="checkbox" checked={events.includes(item.value)} onChange={(event) => setEvents((current) => event.target.checked ? [...current, item.value] : current.filter((value) => value !== item.value))} /><span><strong>{item.value}</strong><small>{item.detail}</small></span></label>)}</div></fieldset>
      <label className="channel-enabled"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>Enabled</strong><small>Run this command for the selected events.</small></span></label>
      <div className="channel-placeholders"><div className="channel-placeholders__head"><div><h3>Placeholders</h3><p>Click a chip to copy it into your command.</p></div></div><div className="placeholder-list">{PLACEHOLDERS.map((item) => <div className="placeholder-row" key={item.value}><code>{item.value}</code><span>{item.detail}</span><CopyButton text={item.value} label="Copy" /></div>)}</div></div>
      {formError ? <div className="error-note" role="alert">{formError}</div> : null}
      <div className="dialog__row"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn--primary" disabled={busy}>{busy ? "Saving…" : channel ? "Save changes" : "Create channel"}</button></div>
    </form>
  </Dialog>;
}

function apiIssue(error: unknown): string {
  if (error instanceof Error && "code" in error) { const e = error as ApiError; return `${e.code}: ${e.message}`; }
  return error instanceof Error ? error.message : "Something went wrong.";
}
