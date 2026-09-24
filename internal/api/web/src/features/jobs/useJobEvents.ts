import { useCallback, useEffect, useState } from "react";
import { buildIntelJobEventsURL } from "./api";
import type { JobProgressFrame } from "./types";

// WP08 §7 SSE client for one job (internal/api/handler_intel.go:145).
//
// Frame grammar of the endpoint:
//   event: progress   data: {"done":..,"failed":..,"skipped":..,"total":..,"status":".."}
//   event: end        (no data, the stream is finished)
//   : keep-alive      (comment heartbeat every 15s)
//
// The first frame after connecting replays the current counters, so a
// reconnecting client does not have to poll GET /jobs/{id} first.

export type JobStreamState = "idle" | "connecting" | "live" | "reconnecting" | "ended" | "error";

export type JobStream = {
  progress: JobProgressFrame | null;
  state: JobStreamState;
  reconnect: () => void;
};

const TERMINAL_JOB_STATUSES = ["succeeded", "partial", "failed", "canceled"];

/** store.Job.Terminal — a terminal job never publishes another SSE frame. */
export function isTerminalJobStatus(status: string | undefined): boolean {
  return status !== undefined && TERMINAL_JOB_STATUSES.includes(status);
}

function finiteOrUndefined(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

/** Parses one `progress` payload; an unparsable frame is ignored. */
export function parseProgressFrame(raw: string): JobProgressFrame | null {
  if (!raw) {
    return null;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    return null;
  }
  const record = parsed as Record<string, unknown>;
  const frame: JobProgressFrame = {
    done: finiteOrUndefined(record.done),
    failed: finiteOrUndefined(record.failed),
    skipped: finiteOrUndefined(record.skipped),
    total: finiteOrUndefined(record.total),
  };
  if (typeof record.status === "string") {
    frame.status = record.status;
  }
  return frame;
}

/**
 * Subscribes to the progress stream of one job while `enabled` is true.
 *
 * EventSource reconnects on its own for transport failures (readyState
 * CONNECTING after an error). A failed handshake — a non-2xx response or a
 * wrong content type — closes the stream for good (readyState CLOSED), which is
 * surfaced as `error` together with a manual `reconnect()`; the caller keeps a
 * slow polling fallback in that case. The stream is always closed on unmount.
 */
export function useIntelJobEvents(jobID: string, enabled: boolean): JobStream {
  const [progress, setProgress] = useState<JobProgressFrame | null>(null);
  const [state, setState] = useState<JobStreamState>(enabled ? "connecting" : "idle");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!enabled || !jobID) {
      return;
    }
    const source = new EventSource(buildIntelJobEventsURL(jobID));

    source.onopen = () => {
      setState("live");
    };

    const handleProgress = (event: Event) => {
      const frame = parseProgressFrame((event as MessageEvent<string>).data);
      if (!frame) {
        return;
      }
      setProgress(frame);
      setState("live");
    };

    // A terminal frame is followed by `event: end` and the server closing the
    // connection; the flag keeps that trailing close from being reported as an
    // error (and from reopening the stream).
    let finished = false;

    const handleEnd = () => {
      finished = true;
      source.close();
      setState("ended");
    };

    const handleError = () => {
      if (finished) {
        return;
      }
      // CONNECTING means the browser retries the connection on its own;
      // CLOSED means the handshake failed (for example an unauthorized
      // ?access_token) and only a manual reconnect can help.
      setState(source.readyState === EventSource.CLOSED ? "error" : "reconnecting");
    };

    source.addEventListener("progress", handleProgress);
    source.addEventListener("end", handleEnd);
    source.addEventListener("error", handleError);

    return () => {
      source.close();
    };
  }, [enabled, jobID, attempt]);

  const reconnect = useCallback(() => {
    setAttempt((value) => value + 1);
  }, []);

  return { progress, state, reconnect };
}
