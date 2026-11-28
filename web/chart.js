// Tiny dependency-free SVG line chart.
const NS = "http://www.w3.org/2000/svg";

function el(name, attrs = {}, parent) {
  const n = document.createElementNS(NS, name);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
  if (parent) parent.appendChild(n);
  return n;
}

export const fmt = new Intl.NumberFormat("zh-CN");

// niceTicks returns ~count evenly spaced "round" values covering [0, max].
export function niceTicks(max, count = 4) {
  if (!(max > 0)) return [0, 1];
  const raw = max / count;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? 10 * mag;
  const ticks = [];
  for (let v = 0; v < max + step * 0.999; v += step) ticks.push(Math.round(v * 1e6) / 1e6);
  if (ticks.length < 2) ticks.push(step);
  return ticks;
}

function shortDay(day) {
  const [, m, d] = day.split("-");
  return `${Number(m)}/${Number(d)}`;
}

/**
 * renderChart draws rows ([{day, dau, wau, mau, ...}], oldest first).
 * series: [{key, label, color}] (only visible ones).
 */
export function renderChart(container, rows, series) {
  container.replaceChildren();
  const width = Math.max(container.clientWidth, 280);
  const height = Math.max(container.clientHeight, 200);
  const pad = { top: 10, right: 12, bottom: 26, left: 44 };
  const svg = el("svg", { viewBox: `0 0 ${width} ${height}`, role: "img" }, container);

  if (!rows.length || !series.length) {
    el("text", { x: width / 2, y: height / 2, "text-anchor": "middle", class: "empty" }, svg).textContent =
      rows.length ? "请选择至少一个指标" : "暂无数据";
    return;
  }

  const max = Math.max(1, ...rows.flatMap((r) => series.map((s) => r[s.key] ?? 0)));
  const ticks = niceTicks(max);
  const yMax = ticks[ticks.length - 1];
  const iw = width - pad.left - pad.right;
  const ih = height - pad.top - pad.bottom;
  const x = (i) => pad.left + (rows.length === 1 ? iw / 2 : (i / (rows.length - 1)) * iw);
  const y = (v) => pad.top + ih - (v / yMax) * ih;

  const grid = el("g", { class: "grid" }, svg);
  const axis = el("g", { class: "axis" }, svg);
  for (const t of ticks) {
    el("line", { x1: pad.left, x2: width - pad.right, y1: y(t), y2: y(t) }, grid);
    el("text", { x: pad.left - 8, y: y(t) + 4, "text-anchor": "end" }, axis).textContent = fmt.format(t);
  }
  const labelEvery = Math.max(1, Math.ceil(rows.length / Math.max(2, Math.floor(iw / 64))));
  rows.forEach((r, i) => {
    if (i % labelEvery !== 0 && i !== rows.length - 1) return;
    if (i !== rows.length - 1 && rows.length - 1 - i < labelEvery * 0.8) return; // avoid overlap with last label
    el("text", { x: x(i), y: height - 6, "text-anchor": "middle" }, axis).textContent = r.tick || shortDay(r.day);
  });

  const g = el("g", { class: "series" }, svg);
  for (const s of series) {
    const d = rows.map((r, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(r[s.key] ?? 0).toFixed(1)}`).join("");
    el("path", { d }, g).style.stroke = s.color; // CSSOM, allowed by CSP (no inline style attrs)
    if (rows.length <= 45) {
      rows.forEach((r, i) => (el("circle", { cx: x(i), cy: y(r[s.key] ?? 0), r: 2.5 }, g).style.fill = s.color));
    }
  }

  // Hover / touch tooltip.
  const hoverLine = el("line", { class: "hover-line", y1: pad.top, y2: pad.top + ih, visibility: "hidden" }, svg);
  const tip = document.createElement("div");
  tip.className = "tooltip";
  tip.hidden = true;
  container.appendChild(tip);
  const show = (clientX) => {
    const rect = svg.getBoundingClientRect();
    const px = ((clientX - rect.left) / rect.width) * width;
    const i = rows.length === 1 ? 0 : Math.round(((px - pad.left) / iw) * (rows.length - 1));
    if (i < 0 || i >= rows.length) return hide();
    const r = rows[i];
    hoverLine.setAttribute("x1", x(i));
    hoverLine.setAttribute("x2", x(i));
    hoverLine.setAttribute("visibility", "visible");
    tip.replaceChildren();
    const b = document.createElement("b");
    b.textContent = r.label || r.day;
    tip.appendChild(b);
    for (const s of series) {
      const row = document.createElement("div");
      const name = document.createElement("span");
      const dot = document.createElement("i");
      dot.style.background = s.color;
      name.append(dot, s.label);
      const val = document.createElement("span");
      val.textContent = fmt.format(r[s.key] ?? 0);
      row.append(name, val);
      tip.appendChild(row);
    }
    tip.hidden = false;
    const left = (x(i) / width) * rect.width;
    const tw = tip.offsetWidth;
    tip.style.left = `${Math.min(Math.max(left - tw / 2, 0), rect.width - tw)}px`;
    tip.style.top = `0px`;
  };
  const hide = () => {
    tip.hidden = true;
    hoverLine.setAttribute("visibility", "hidden");
  };
  svg.addEventListener("pointermove", (e) => show(e.clientX));
  svg.addEventListener("pointerdown", (e) => show(e.clientX));
  svg.addEventListener("pointerleave", hide);
}
