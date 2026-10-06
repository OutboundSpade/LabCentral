import { browser } from "$app/environment";
import { readable } from "svelte/store";

export const now = readable(Date.now(), (set) => {
  if (!browser) return;
  set(Date.now());
  const timer = setInterval(() => set(Date.now()), 1000);
  return () => clearInterval(timer);
});

export function duration(ms: number) {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.floor(seconds / 60);
  return minutes < 60
    ? `${minutes} m`
    : `${Math.floor(minutes / 60)} h ${minutes % 60} m`;
}

export function relative(value: string, current: number) {
  const elapsed = current - Date.parse(value);
  return Number.isFinite(elapsed)
    ? `${duration(elapsed)} ago`
    : "Time unavailable";
}
