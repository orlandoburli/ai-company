"use client";

import { useCallback, useEffect, useRef } from "react";
import {
  ApiError,
  api,
  isTerminalJobState,
  openJobEvents,
  type Job,
  type LogEntry,
} from "./api";

/**
 * Live feeds for non-terminal jobs: one EventSource per job on
 * /api/jobs/{id}/events, falling back to polling /logs (and reading the
 * returned `job`) every 1.5s when the stream errors, per the M2 contract.
 *
 * The hook owns the connections; the caller stays the owner of job state and
 * receives deltas through the callbacks. Callers must render server error
 * messages — ApiError envelopes are surfaced on onStreamError, never swallowed.
 */
export interface JobStreamCallbacks {
  /** A state frame (or a polled refresh) delivered a newer job row. */
  onJobUpdated: (job: Job) => void;
  /** A log row arrived (SSE frame or poll page). */
  onLogChunk: (jobId: string, entry: LogEntry) => void;
  /** The stream closed with `done`; the job should be terminal now. */
  onDone: (jobId: string) => void;
  /** The stream errored and polling could not recover; show this to the user. */
  onStreamError: (jobId: string, message: string) => void;
}

const POLL_INTERVAL_MS = 1500;

interface Feed {
  /** Close function of the EventSource, while in SSE mode. */
  close?: () => void;
  pollTimer?: ReturnType<typeof setTimeout>;
  closed: boolean;
  polling: boolean;
  cursor: number;
  pollFailures: number;
}

export function useJobStreams(
  jobs: Job[] | null,
  cb: JobStreamCallbacks,
): void {
  const feedsRef = useRef<Map<string, Feed>>(new Map());

  // The freshest callbacks without reconnecting on every render.
  const cbRef = useRef(cb);
  useEffect(() => {
    cbRef.current = cb;
  });

  const settleAfterDone = useCallback(async (id: string, feed: Feed) => {
    // `done` carries no payload; the last state frame is authoritative in
    // practice, but fetch once so a missed transition cannot hang the UI.
    try {
      const r = await api.getJob(id);
      cbRef.current.onJobUpdated(r.job);
    } catch {
      // The refresh on `done` below still runs; a failed settle is not fatal.
    }
    feed.closed = true;
    cbRef.current.onDone(id);
  }, []);

  const startPolling = useCallback(
    (id: string, feed: Feed) => {
      if (feed.polling || feed.closed) return;
      feed.polling = true;

      const tick = async () => {
        if (feed.closed) return;
        try {
          const r = await api.jobLogs(id, feed.cursor);
          for (const entry of r.logs) {
            if (entry.seq > feed.cursor) feed.cursor = entry.seq;
            cbRef.current.onLogChunk(id, entry);
          }
          cbRef.current.onJobUpdated(r.job);
          feed.pollFailures = 0;
          if (isTerminalJobState(r.job.state)) {
            feed.closed = true;
            cbRef.current.onDone(id);
            return;
          }
          feed.pollTimer = setTimeout(() => void tick(), POLL_INTERVAL_MS);
        } catch (err) {
          feed.pollFailures += 1;
          if (
            err instanceof ApiError &&
            (err.status === 404 || err.status === 401 || err.status === 403)
          ) {
            feed.closed = true;
            cbRef.current.onStreamError(id, err.message);
            cbRef.current.onDone(id);
            return;
          }
          if (feed.pollFailures >= 4) {
            feed.closed = true;
            cbRef.current.onStreamError(
              id,
              err instanceof Error
                ? err.message
                : "Lost the connection to the job stream.",
            );
            cbRef.current.onDone(id);
            return;
          }
          feed.pollTimer = setTimeout(() => void tick(), POLL_INTERVAL_MS);
        }
      };

      void tick();
    },
    [],
  );

  const start = useCallback(
    (id: string) => {
      const feeds = feedsRef.current;
      if (feeds.has(id)) return;
      const feed: Feed = { closed: false, polling: false, cursor: 0, pollFailures: 0 };
      feeds.set(id, feed);

      feed.close = openJobEvents(id, feed.cursor, {
        onLog: (entry) => {
          if (entry.seq > feed.cursor) feed.cursor = entry.seq;
          cbRef.current.onLogChunk(id, entry);
        },
        onState: (job) => cbRef.current.onJobUpdated(job),
        onDone: () => void settleAfterDone(id, feed),
        onError: () => {
          // Contract: stop the stream and poll /logs + /jobs instead.
          feed.close = undefined;
          startPolling(id, feed);
        },
      });
    },
    [settleAfterDone, startPolling],
  );

  const stop = useCallback((id: string) => {
    const feed = feedsRef.current.get(id);
    if (!feed) return;
    feed.closed = true;
    feed.polling = true; // stop any in-flight poll loop
    if (feed.pollTimer) clearTimeout(feed.pollTimer);
    feed.close?.();
    feedsRef.current.delete(id);
  }, []);

  // Attach/detach to match the non-terminal jobs the caller knows about.
  const activeIds = jobs
    ?.filter((j) => !isTerminalJobState(j.state))
    .map((j) => j.id)
    .sort()
    .join(",");

  useEffect(() => {
    const wanted = new Set(activeIds ? activeIds.split(",") : []);
    for (const id of Array.from(feedsRef.current.keys())) {
      if (!wanted.has(id)) stop(id);
    }
    for (const id of wanted) {
      if (!feedsRef.current.has(id)) start(id);
    }
  }, [activeIds, start, stop]);

  // Unmount: close everything. Capture the map outside the cleanup so the
  // effect does not read a ref that could have been replaced.
  useEffect(() => {
    const feeds = feedsRef.current;
    return () => {
      for (const feed of feeds.values()) {
        feed.closed = true;
        feed.polling = true; // stop any in-flight poll loop
        if (feed.pollTimer) clearTimeout(feed.pollTimer);
        feed.close?.();
      }
      feeds.clear();
    };
  }, []);
}
