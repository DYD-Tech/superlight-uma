// UTC date helpers and chart-series bucketing. Days are "YYYY-MM-DD".
// Counting is not done here: values come from daily_summary snapshots
// (and today's live /v1/stats). A week/month point is the WAU/MAU already
// stored on a day (7-day / 30-day distinct count ending that day), never
// a sum of daily values.

const DAY_MS = 86400000;

export function parseDay(day) {
  const [y, m, d] = day.split("-").map(Number);
  return Date.UTC(y, m - 1, d);
}

export function formatDay(ms) {
  const t = new Date(ms);
  const y = t.getUTCFullYear();
  const m = String(t.getUTCMonth() + 1).padStart(2, "0");
  const d = String(t.getUTCDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

export function utcToday(now = Date.now()) {
  return formatDay(now);
}

export function addDays(day, n) {
  return formatDay(parseDay(day) + n * DAY_MS);
}

export function daysBetween(a, b) {
  return Math.round((parseDay(b) - parseDay(a)) / DAY_MS);
}

export function shortDay(day) {
  const [, m, d] = day.split("-");
  return `${Number(m)}/${Number(d)}`;
}

// ISO week: Monday = start, Sunday = end (UTC).
export function weekStartMonday(day) {
  const dow = new Date(parseDay(day)).getUTCDay(); // 0 Sun .. 6 Sat
  return addDays(day, dow === 0 ? -6 : 1 - dow);
}

export function weekEndSunday(day) {
  return addDays(weekStartMonday(day), 6);
}

export function monthStart(day) {
  return `${day.slice(0, 7)}-01`;
}

export function monthEnd(day) {
  const [y, m] = day.split("-").map(Number);
  return formatDay(Date.UTC(y, m, 0));
}

export function addMonths(day, n) {
  const [y, m, d] = day.split("-").map(Number);
  return formatDay(Date.UTC(y, m - 1 + n, d));
}

export function yearMonth(day) {
  return day.slice(0, 7);
}

// How many daily history rows we need to fill the last 12 calendar months.
export function historyDaysNeeded(today) {
  const start = monthStart(addMonths(monthStart(today), -11));
  return daysBetween(start, today) + 1;
}

export function historyRangeDays(today) {
  return Math.min(3650, historyDaysNeeded(today) + 2);
}

export function indexByDay(rows) {
  const map = new Map();
  for (const r of rows || []) {
    if (!r?.day) continue;
    map.set(r.day, {
      day: r.day,
      dau: r.dau ?? 0,
      wau: r.wau ?? 0,
      mau: r.mau ?? 0,
      total: r.total ?? 0,
    });
  }
  return map;
}

// Overwrite (or insert) today's live /v1/stats so the current bucket is
// not missing or stale before the hourly rollup runs.
export function mergeLive(byDay, live) {
  if (!live?.day) return byDay;
  byDay.set(live.day, {
    day: live.day,
    dau: live.dau ?? 0,
    wau: live.wau ?? 0,
    mau: live.mau ?? 0,
    total: live.total ?? 0,
  });
  return byDay;
}

// Prefer the snapshot on preferDay (bucket end). If that day was never
// rolled up, use the latest available day in [start, end]. If none, 0.
export function bucketValue(byDay, start, end, key, preferDay) {
  const preferred = preferDay && byDay.get(preferDay);
  if (preferred) return { value: preferred[key] ?? 0, source: preferDay };
  for (let d = end; d >= start; d = addDays(d, -1)) {
    const row = byDay.get(d);
    if (row) return { value: row[key] ?? 0, source: d };
  }
  return { value: 0, source: null };
}

export function dailyTable(byDay, today, count = 30) {
  const rows = [];
  const start = addDays(today, -(count - 1));
  for (let d = start; d <= today; d = addDays(d, 1)) {
    const r = byDay.get(d);
    rows.push({
      day: d,
      tick: shortDay(d),
      label: d,
      dau: r?.dau ?? 0,
      wau: r?.wau ?? 0,
      mau: r?.mau ?? 0,
      total: r?.total ?? 0,
    });
  }
  return rows;
}

export function weeklySeries(byDay, today, key = "wau", count = 12) {
  const thisMonday = weekStartMonday(today);
  const firstMonday = addDays(thisMonday, -7 * (count - 1));
  const rows = [];
  for (let i = 0; i < count; i++) {
    const start = addDays(firstMonday, 7 * i);
    const end = addDays(start, 6);
    const current = start <= today && today <= end;
    const picked = current
      ? { value: byDay.get(today)?.[key] ?? 0, source: today }
      : bucketValue(byDay, start, end, key, end);
    rows.push({
      day: start,
      tick: shortDay(start),
      label: current ? `${start} – ${end}（本周实时）` : `${start} – ${end}`,
      [key]: picked.value,
    });
  }
  return rows;
}

export function monthlySeries(byDay, today, key = "mau", count = 12) {
  const first = monthStart(addMonths(monthStart(today), -(count - 1)));
  const rows = [];
  for (let i = 0; i < count; i++) {
    const start = addMonths(first, i);
    const end = monthEnd(start);
    const current = yearMonth(start) === yearMonth(today);
    const picked = current
      ? { value: byDay.get(today)?.[key] ?? 0, source: today }
      : bucketValue(byDay, start, end, key, end);
    const ym = yearMonth(start);
    rows.push({
      day: start,
      tick: ym,
      label: current ? `${ym}（本月实时）` : ym,
      [key]: picked.value,
    });
  }
  return rows;
}

export function buildDashboardSeries(historyDays, live, today) {
  const day = live?.day || today;
  const byDay = mergeLive(indexByDay(historyDays), live);
  const daily = dailyTable(byDay, day, 30);
  return {
    today: day,
    daily,
    wau: weeklySeries(byDay, day, "wau", 12),
    mau: monthlySeries(byDay, day, "mau", 12),
  };
}
