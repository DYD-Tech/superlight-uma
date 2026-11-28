import assert from "node:assert/strict";
import {
  addDays,
  addMonths,
  bucketValue,
  buildDashboardSeries,
  dailyTable,
  daysBetween,
  formatDay,
  historyDaysNeeded,
  historyRangeDays,
  indexByDay,
  mergeLive,
  monthEnd,
  monthStart,
  parseDay,
  weekEndSunday,
  weekStartMonday,
  weeklySeries,
  monthlySeries,
  utcToday,
} from "../web/series.js";

function row(day, wau = 0, mau = 0, dau = 0, total = 0) {
  return { day, dau, wau, mau, total };
}

// --- date helpers (all UTC) ---
assert.equal(formatDay(parseDay("2026-11-27")), "2026-11-27");
assert.equal(addDays("2026-11-27", 1), "2026-11-28");
assert.equal(addDays("2026-12-31", 1), "2027-01-01");
assert.equal(daysBetween("2025-12-01", "2026-11-28"), 362);
assert.equal(utcToday(Date.UTC(2026, 10, 28, 23, 30)), "2026-11-28");
// Local-looking evening in a UTC+0 / UTC+8 TZ must still use UTC.
assert.equal(utcToday(Date.UTC(2026, 10, 28, 0, 30)), "2026-11-28");

// 2026-11-27 is a Friday; 2026-11-23 Mon .. 2026-11-29 Sun.
assert.equal(weekStartMonday("2026-11-27"), "2026-11-23");
assert.equal(weekEndSunday("2026-11-27"), "2026-11-29");
assert.equal(weekStartMonday("2026-11-23"), "2026-11-23");
assert.equal(weekStartMonday("2026-11-29"), "2026-11-23");
assert.equal(weekStartMonday("2026-11-22"), "2026-11-16"); // Sunday

assert.equal(monthStart("2026-11-28"), "2026-11-01");
assert.equal(monthEnd("2026-11-28"), "2026-11-30");
assert.equal(monthEnd("2026-02-01"), "2026-02-28");
assert.equal(monthEnd("2028-02-01"), "2028-02-29");
assert.equal(addMonths("2026-11-01", -11), "2025-12-01");
assert.equal(historyDaysNeeded("2026-11-28"), 363);
assert.ok(historyRangeDays("2026-11-28") >= 363);

// --- merge live overwrites today's rollup ---
const byDay = mergeLive(
  indexByDay([row("2026-11-27", 4, 4, 4, 4), row("2026-11-28", 4, 4, 4, 4)]),
  { day: "2026-11-28", dau: 5, wau: 5, mau: 5, total: 5 },
);
assert.equal(byDay.get("2026-11-28").dau, 5);
assert.equal(byDay.get("2026-11-27").dau, 4);

// --- daily 30-day window is fixed and zero-filled ---
const daily = dailyTable(byDay, "2026-11-28", 30);
assert.equal(daily.length, 30);
assert.equal(daily[0].day, "2026-10-30");
assert.equal(daily[0].dau, 0);
assert.equal(daily[daily.length - 2].day, "2026-11-27");
assert.equal(daily[daily.length - 2].dau, 4);
assert.equal(daily[daily.length - 1].day, "2026-11-28");
assert.equal(daily[daily.length - 1].dau, 5);
assert.equal(daily.filter((r) => r.dau === 0).length, 28);

// --- bucket fallback: prefer end day, else latest in range, else 0 ---
const sparse = indexByDay([row("2026-11-21", 3, 0, 0, 0), row("2026-11-20", 2)]);
assert.deepEqual(bucketValue(sparse, "2026-11-16", "2026-11-22", "wau", "2026-11-22"), {
  value: 3,
  source: "2026-11-21",
});
assert.deepEqual(bucketValue(indexByDay([row("2026-11-22", 9)]), "2026-11-16", "2026-11-22", "wau", "2026-11-22"), {
  value: 9,
  source: "2026-11-22",
});
assert.deepEqual(bucketValue(indexByDay([]), "2026-11-16", "2026-11-22", "wau", "2026-11-22"), {
  value: 0,
  source: null,
});

// --- weekly: 12 ISO weeks, Sunday snapshot, current week uses live today ---
// today = Friday 2026-11-28. Current week 11/23–11/29 uses live 5.
// Previous week 11/16–11/22: no Sunday row, latest is 11/21 WAU=3.
// Week 11/09–11/15: empty → 0.
const weeks = weeklySeries(byDay, "2026-11-28", "wau", 12);
assert.equal(weeks.length, 12);
assert.equal(weeks[0].day, "2026-09-07"); // Monday 11 weeks before 2026-11-23
assert.equal(weeks[11].day, "2026-11-23");
assert.equal(weeks[11].wau, 5);
assert.match(weeks[11].label, /本周实时/);
const prev = weeks[10];
assert.equal(prev.day, "2026-11-16");
// byDay only has 11/27 and 11/28, so week 11/16–11/22 is 0
assert.equal(prev.wau, 0);

const weeks2 = weeklySeries(sparse, "2026-11-28", "wau", 2);
assert.equal(weeks2[0].day, "2026-11-16");
assert.equal(weeks2[0].wau, 3); // fallback to 11/21
assert.equal(weeks2[1].wau, 0); // current week, no live today in sparse

// A completed week uses the Sunday WAU, not a sum of daily values.
const summedTrap = indexByDay([
  row("2026-11-16", 1, 0, 1, 1),
  row("2026-11-17", 1, 0, 1, 1),
  row("2026-11-22", 2, 0, 1, 1), // Sunday WAU snapshot is 2, not 1+1+1
]);
const oneWeek = weeklySeries(summedTrap, "2026-11-28", "wau", 2);
assert.equal(oneWeek[0].wau, 2);

// --- monthly: 12 calendar months, last-day snapshot, current month live ---
const monthsMap = mergeLive(
  indexByDay([
    row("2025-12-31", 0, 10, 0, 10),
    row("2026-01-15", 0, 8, 0, 11), // Jan 31 missing → fallback to 15th
    row("2026-10-31", 0, 20, 0, 40),
    row("2026-11-27", 0, 4, 4, 4),
  ]),
  { day: "2026-11-28", dau: 5, wau: 5, mau: 5, total: 5 },
);
const months = monthlySeries(monthsMap, "2026-11-28", "mau", 12);
assert.equal(months.length, 12);
assert.equal(months[0].day, "2025-12-01");
assert.equal(months[0].tick, "2025-12");
assert.equal(months[0].mau, 10);
assert.equal(months[1].tick, "2026-01");
assert.equal(months[1].mau, 8); // fallback
assert.equal(months[10].tick, "2026-10");
assert.equal(months[10].mau, 20);
assert.equal(months[11].tick, "2026-11");
assert.equal(months[11].mau, 5); // live today, not 4
assert.match(months[11].label, /本月实时/);
// February 2026 has no rows → 0, axis is not cropped
assert.equal(months[2].tick, "2026-02");
assert.equal(months[2].mau, 0);

// Full builder: 30 daily points + 12 weeks + 12 months, live today applied
const built = buildDashboardSeries(
  [row("2026-11-27", 4, 4, 4, 4)],
  { day: "2026-11-28", dau: 5, wau: 5, mau: 5, total: 5 },
  "2026-11-28",
);
assert.equal(built.daily.length, 30);
assert.equal(built.daily[29].total, 5);
assert.equal(built.wau.length, 12);
assert.equal(built.mau.length, 12);
assert.equal(built.wau[11].wau, 5);
assert.equal(built.mau[11].mau, 5);
// Axis must start on the first window day, not the first data day (11/27).
assert.equal(built.daily[0].day, "2026-10-30");
assert.equal(built.daily[0].dau, 0);

console.log("series tests ok");
