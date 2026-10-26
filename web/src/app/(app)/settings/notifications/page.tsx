"use client";

import { FormEvent, useMemo, useState } from "react";
import {
  api,
  type NotificationChannel,
  type NotificationChannelInput,
  type NotificationEvent,
} from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useConfirm } from "@/lib/confirm";
import { useSession } from "@/lib/session";
import { SettingsNav } from "@/components/settings-nav";
import { CopyButton, Dialog, EmptyState, ErrorBlock, Field, Skeleton, Toasts } from "@/components/primitives";
import { apiErrorMessage } from "@/components/primitives";
import { useT } from "@/lib/i18n";
import type { TranslationKey } from "@/lib/locales";

const EVENTS: { value: NotificationEvent; label: TranslationKey; detail: TranslationKey }[] = [
  { value: "checkpoint_pending", label: "settings.notifications.checkpointPending", detail: "settings.notifications.checkpointPendingHint" },
  { value: "run_failed", label: "settings.notifications.runFailed", detail: "settings.notifications.runFailedHint" },
  { value: "run_completed", label: "settings.notifications.runCompleted", detail: "settings.notifications.runCompletedHint" },
];

const PLACEHOLDERS: { value: string; detail: TranslationKey }[] = [
  { value: "{{event.kind}}", detail: "settings.notifications.eventNameHint" },
  { value: "{{run.id}}", detail: "settings.notifications.runIdHint" },
  { value: "{{item.id}}", detail: "settings.notifications.itemIdHint" },
  { value: "{{title}}", detail: "settings.notifications.titleHint" },
  { value: "{{body}}", detail: "settings.notifications.bodyHint" },
  { value: "{{link}}", detail: "settings.notifications.linkHint" },
];

export default function NotificationChannelsPage() {
  const t = useT();
  const { me } = useSession();
  const { toasts, push } = useToasts();
  const confirm = useConfirm();
  const channelsQ = useAsync(() => api.listNotificationChannels().then((r) => r.channels), []);
  const [editing, setEditing] = useState<NotificationChannel | "new" | null>(null);
  const isManager = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const channels = useMemo(() => [...(channelsQ.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [channelsQ.data]);

  async function remove(channel: NotificationChannel) {
    if (!await confirm({ title: t("settings.notifications.deleteTitle"), message: t("settings.notifications.deleteMessage", { name: channel.name }), confirmLabel: t("settings.notifications.delete"), destructive: true })) return;
    try {
      await api.deleteNotificationChannel(channel.id);
      await channelsQ.reload();
      push("info", t("settings.notifications.deleted", { name: channel.name }));
    } catch (error) { push("error", apiIssue(error)); }
  }

  return <div className="notification-settings-page">
    <div className="pagehead">
      <h1>{t("settings.notifications.title")}</h1>
      <span className="pagehead__sub">{t("settings.notifications.subtitle")}</span>
    </div>
    <SettingsNav />

    <div className="notification-safety">
      <span className="notification-safety__mark" aria-hidden>!</span>
      <div><strong>{t("settings.notifications.safetyTitle")}</strong><p>{t("settings.notifications.safetyBody")}</p></div>
    </div>

    <section className="section">
      <div className="section__head">
        <div><h2>{t("settings.notifications.channels")}</h2><p className="tiny muted" style={{ margin: "3px 0 0" }}>{t("settings.notifications.channelsHint")}</p></div>
        <button type="button" className="btn btn--sm btn--primary" style={{ marginLeft: "auto" }} onClick={() => setEditing("new")}>{t("settings.notifications.new")}</button>
      </div>
      {!isManager ? <p className="notification-role-note">{t("settings.notifications.roleNote", { role: me?.tenant.role ? roleLabel(me.tenant.role, t) : t("settings.notifications.readOnlyRole") })}</p> : null}
      <div className="section__body">
        {channelsQ.error ? <ErrorBlock title={t("settings.notifications.loadError")} message={apiIssue(channelsQ.error)} retry={() => void channelsQ.reload()} /> : !channelsQ.data ? <Skeleton h={110} /> : channels.length === 0 ? <EmptyState title={t("settings.notifications.emptyTitle")} body={t("settings.notifications.emptyBody")} /> : (
          <div className="table-wrap">
            <table className="data notification-channels-table">
              <thead><tr><th scope="col">{t("settings.notifications.name")}</th><th scope="col">{t("settings.notifications.events")}</th><th scope="col">{t("settings.notifications.enabled")}</th><th scope="col">{t("settings.notifications.commandTemplate")}</th><th scope="col"><span className="visually-hidden">{t("settings.notifications.actions")}</span></th></tr></thead>
              <tbody>{channels.map((channel) => <tr key={channel.id}>
                <td className="notification-channel-name">{channel.name}</td>
                <td><div className="notification-event-list">{channel.events.map((event) => <span key={event} className="badge-outline">{t(`notificationKind.${event}` as TranslationKey)}</span>)}</div></td>
                <td><span className={`notification-enabled ${channel.enabled ? "notification-enabled--on" : ""}`}><i aria-hidden />{channel.enabled ? t("settings.notifications.enabled") : t("settings.notifications.off")}</span></td>
                <td><code className="notification-command" title={channel.command_template}>{channel.command_template}</code></td>
                <td><div className="stack" style={{ gap: 6 }}><button type="button" className="btn btn--sm" onClick={() => setEditing(channel)}>{t("settings.notifications.edit")}</button><button type="button" className="btn btn--sm btn--ghost btn--danger" onClick={() => void remove(channel)}>{t("settings.notifications.delete")}</button></div></td>
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
      push("info", t("settings.notifications.saved"));
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
  const t = useT();
  const [name, setName] = useState(channel?.name ?? "");
  const [command, setCommand] = useState(channel?.command_template ?? "");
  const [events, setEvents] = useState<NotificationEvent[]>(channel?.events ?? []);
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim() || !command.trim() || events.length === 0) {
      setFormError(t("settings.notifications.formRequired"));
      return;
    }
    setBusy(true);
    setFormError("");
    try { await onSave({ name: name.trim(), command_template: command.trim(), events, enabled }); }
    catch (error) { const message = apiIssue(error); setFormError(message); onError(error); }
    finally { setBusy(false); }
  }

  return <Dialog title={channel ? t("settings.notifications.editTitle") : t("settings.notifications.newTitle")} description={t("settings.notifications.dialogDescription")} onClose={onClose} width={760}>
    <div className="notification-admin-note"><strong>{t("settings.notifications.configureWho")}</strong> {t("settings.notifications.configureHint")}</div>
    <form className="channel-editor" onSubmit={(event) => void submit(event)}>
      <Field label={t("settings.notifications.channelName")}><input className="input" autoFocus value={name} onChange={(event) => setName(event.target.value)} placeholder={t("settings.notifications.channelPlaceholder")} /></Field>
      <Field label={t("settings.notifications.commandTemplate")} hint={t("settings.notifications.commandHint")}>
        <textarea className="input channel-command-input mono" rows={3} value={command} onChange={(event) => setCommand(event.target.value)} placeholder={'notify-send "{{title}}" "{{body}}"'} />
      </Field>
      <fieldset className="channel-events"><legend>{t("settings.notifications.watchEvents")}</legend><div className="channel-events__list">{EVENTS.map((item) => <label key={item.value} className="channel-event-option"><input type="checkbox" checked={events.includes(item.value)} onChange={(event) => setEvents((current) => event.target.checked ? [...current, item.value] : current.filter((value) => value !== item.value))} /><span><strong>{t(item.label)}</strong><small>{t(item.detail)}</small></span></label>)}</div></fieldset>
      <label className="channel-enabled"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>{t("settings.notifications.enabled")}</strong><small>{t("settings.notifications.enabledHint")}</small></span></label>
      <div className="channel-placeholders"><div className="channel-placeholders__head"><div><h3>{t("settings.notifications.placeholders")}</h3><p>{t("settings.notifications.placeholdersHint")}</p></div></div><div className="placeholder-list">{PLACEHOLDERS.map((item) => <div className="placeholder-row" key={item.value}><code>{item.value}</code><span>{t(item.detail)}</span><CopyButton text={item.value} label={t("settings.notifications.copy")} /></div>)}</div></div>
      {formError ? <div className="error-note" role="alert">{formError}</div> : null}
      <div className="dialog__row"><button type="button" className="btn" onClick={onClose}>{t("settings.notifications.cancel")}</button><button type="submit" className="btn btn--primary" disabled={busy}>{busy ? t("settings.notifications.saving") : channel ? t("settings.notifications.saveChanges") : t("settings.notifications.create")}</button></div>
    </form>
  </Dialog>;
}

function apiIssue(error: unknown): string {
  return apiErrorMessage(error);
}

function roleLabel(role: string, t: ReturnType<typeof useT>): string {
  switch (role) {
    case "owner": return t("settings.members.owner");
    case "admin": return t("settings.members.admin");
    case "member": return t("settings.members.member");
    case "guest": return t("settings.members.guest");
    default: return role;
  }
}
