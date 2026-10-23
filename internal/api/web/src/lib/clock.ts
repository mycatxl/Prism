import { useSyncExternalStore } from "react";

// One shared wall clock for every "remaining time" readout: components must
// not call `Date.now()` during render (render stays pure), so the ticking
// happens here, outside React, and subscribers re-render on the tick.
let currentTime = Date.now();
let clockTimer: ReturnType<typeof setInterval> | undefined;
const clockSubscribers = new Set<() => void>();

function subscribeClock(listener: () => void) {
  if (clockSubscribers.size === 0) {
    currentTime = Date.now();
    clockTimer = setInterval(() => {
      currentTime = Date.now();
      clockSubscribers.forEach((notify) => notify());
    }, 1000);
  }
  clockSubscribers.add(listener);
  return () => {
    clockSubscribers.delete(listener);
    if (clockSubscribers.size === 0) {
      clearInterval(clockTimer);
      clockTimer = undefined;
    }
  };
}

const clockSnapshot = () => currentTime;
const serverClockSnapshot = () => 0;

/** The current wall time, re-announced once per second while anyone listens. */
export function useNow(): number {
  return useSyncExternalStore(subscribeClock, clockSnapshot, serverClockSnapshot);
}
