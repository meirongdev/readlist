/* readlist 内嵌 SPA —— 零依赖,hash 路由,权重滑块纯客户端重排 */
const $ = (sel) => document.querySelector(sel);
const app = $("#app");

const DIM_ORDER = ["A", "C", "F", "T", "D", "P", "readability"];
const DIM_LABEL = { A: "口碑", C: "技术圈声量", F: "时效", T: "权威", D: "深度", P: "可操作", readability: "馆藏可读性" };
const STATE_LABEL = { measured: "实测", shrunk: "收缩", unknown: "未知" };

let meta = null;
let lists = [];
let currentList = null; // 当前榜的完整口径:weights / bands / order / min_coverage
let currentItems = [];
let weights = {}; // 用户拖动后的权重(currentList.weights 的可变副本)

async function api(path) {
  const r = await fetch(path);
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `HTTP ${r.status}`);
  return r.json();
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
function fmt(n) { return n == null ? "—" : Number(n).toFixed(1); }
function clone(o) { return JSON.parse(JSON.stringify(o ?? {})); }

function gradeBadge(g) { return g ? `<span class="badge badge-${esc(g)}">${esc(g)} 级</span>` : ""; }

function readingBadges(item) {
  if (!item.reading || !item.reading.has_reading) return "";
  const map = { read: ["badge-read", "✓ 已读"], reading: ["badge-reading", "◐ 在读"] };
  let s = "";
  const hit = map[item.reading.status];
  if (hit) s += `<span class="badge ${hit[0]}">${hit[1]}</span>`;
  (item.reading.shelves || []).forEach((sh) => {
    if (sh === "弃读") return; // 弃读默认不公开
    s += `<span class="badge badge-shelf">☆ ${esc(sh)}</span>`;
  });
  return s;
}

/* ---- 权重滑块:纯客户端点积 + band + coverage(与后端 score.Combine 同一套公式) ---- */

function effectiveScore(dim, d, bands) {
  // unknown 维度不参与:它的 score 字段只是占位的 0,不是"这一维得了 0 分"。
  if (!d || d.state === "unknown") return null;
  const b = bands && bands[dim];
  if (b && b.tol > 0) return Math.max(0, 100 * (1 - Math.abs(d.score - b.target) / b.tol));
  return d.score;
}

function rescore(item, bands) {
  let totalW = 0, availW = 0, acc = 0;
  for (const dim in weights) totalW += weights[dim] || 0;
  for (const dim in weights) {
    const w = weights[dim] || 0;
    const eff = effectiveScore(dim, (item.dims || {})[dim], bands);
    if (eff == null) continue;
    availW += w;
    acc += w * eff;
  }
  const coverage = totalW > 0 ? availW / totalW : 0;
  return { tbs: coverage > 0 ? acc / coverage : 0, coverage };
}

function rankedItems() {
  const bands = (currentList && currentList.bands) || {};
  const minCoverage = (currentList && currentList.min_coverage) || 0;
  const asc = currentList && currentList.order === "asc";
  const scored = currentItems.map((it) => Object.assign({}, it, rescore(it, bands)));
  // coverage 是真实的准入条件(后端同样如此):权重调过之后覆盖不足的书要退出这份榜。
  const kept = scored.filter((it) => it.coverage + 1e-9 >= minCoverage);
  kept.sort((a, b) => (a.tbs === b.tbs ? a.work_id.localeCompare(b.work_id) : asc ? a.tbs - b.tbs : b.tbs - a.tbs));
  return { kept, hidden: scored.length - kept.length };
}

function renderRanking() {
  const { kept, hidden } = rankedItems();
  const wrap = document.createElement("div");
  wrap.className = "ranking-wrap";

  if (!kept.length) {
    wrap.innerHTML = `<p class="loading">当前权重下没有书达到覆盖率门槛（min_coverage ${(currentList.min_coverage || 0).toFixed(2)}）。</p>`;
    return wrap;
  }

  const ol = document.createElement("ol");
  ol.className = "ranking";
  kept.forEach((it, i) => {
    const li = document.createElement("li");
    li.className = "book";
    // 位次按当前排序重新编号 —— 拖过滑块之后沿用服务端的原始 rank 是错的。
    li.innerHTML = `
      <div class="book-top">
        <span class="rank-no">${i + 1}</span>
        <span class="book-title"><a href="#/book/${encodeURIComponent(it.work_id)}">${esc(it.title)}</a></span>
        ${gradeBadge(it.grade)}${readingBadges(it)}
        <span class="tbs">${fmt(it.tbs)}<small> 分</small></span>
      </div>
      <div class="book-meta">${esc(it.author || "")}${it.year ? " · " + it.year : ""}${it.topic ? " · " + esc(it.topic) : ""}</div>
      <div class="reason">${it.reason ? "<b>为什么：</b>" + esc(it.reason) : ""}</div>
      <div class="coverage">覆盖 ${Math.round(it.coverage * 100)}%</div>`;
    ol.appendChild(li);
  });
  wrap.appendChild(ol);
  if (hidden > 0) {
    const note = document.createElement("p");
    note.className = "coverage";
    note.textContent = `另有 ${hidden} 本在当前权重下覆盖率不足,已退出本榜。`;
    wrap.appendChild(note);
  }
  return wrap;
}

function repaintRanking() {
  const old = app.querySelector(".ranking-wrap");
  if (old) old.replaceWith(renderRanking());
}

function sliderSection() {
  const defaults = clone(currentList.weights);
  const dims = DIM_ORDER.filter((d) => d in defaults);

  const el = document.createElement("div");
  el.className = "sliders";
  const head = document.createElement("h3");
  head.textContent = `权重滑块（${dims.length} 维,即时重排）`;
  el.appendChild(head);

  for (const dim of dims) {
    const row = document.createElement("div");
    row.className = "slider-row";
    row.innerHTML = `<label>${esc(DIM_LABEL[dim] || dim)}</label>
      <input type="range" min="0" max="1" step="0.05" value="${defaults[dim]}" data-dim="${esc(dim)}">
      <span class="val">${defaults[dim].toFixed(2)}</span>`;
    el.appendChild(row);
  }

  const foot = document.createElement("div");
  foot.className = "slider-foot";
  // 说清楚重排的边界:滑块只在这份榜已选出的书里重排。要把榜外的书选进来,
  // 需要重跑选材(去重 + 多样性约束),那不是客户端点积能做的事。
  foot.innerHTML = `<span class="coverage">在本榜已选出的 ${currentItems.length} 本内重排；
    换权重不会把榜外的书选进来（那需要重跑选材）。</span>`;
  const reset = document.createElement("button");
  reset.className = "reset";
  reset.textContent = "重置";
  reset.onclick = () => {
    weights = clone(defaults);
    el.querySelectorAll("input[data-dim]").forEach((input) => {
      input.value = defaults[input.dataset.dim];
      input.parentElement.querySelector(".val").textContent = defaults[input.dataset.dim].toFixed(2);
    });
    repaintRanking();
  };
  foot.appendChild(reset);
  el.appendChild(foot);

  el.addEventListener("input", (ev) => {
    const dim = ev.target.dataset && ev.target.dataset.dim;
    if (!dim) return;
    weights[dim] = parseFloat(ev.target.value);
    ev.target.parentElement.querySelector(".val").textContent = weights[dim].toFixed(2);
    repaintRanking();
  });
  return el;
}

async function renderHome(presetId) {
  const id = presetId || (lists[0] && lists[0].id);
  if (!id) throw new Error("没有可用的榜单");
  const data = await api(`/api/v1/lists/${encodeURIComponent(id)}`);
  // 口径(weights/bands/order/min_coverage)以单榜响应里的 list 为准。
  currentList = data.list || lists.find((l) => l.id === id) || {};
  currentItems = data.items || [];
  weights = clone(currentList.weights);

  app.innerHTML = "";
  const bar = document.createElement("div");
  bar.className = "preset-bar";
  for (const l of lists) {
    const b = document.createElement("button");
    b.textContent = l.name;
    b.className = l.id === currentList.id ? "active" : "";
    b.onclick = () => { location.hash = `#/list/${l.id}`; };
    bar.appendChild(b);
  }
  app.appendChild(bar);

  const desc = document.createElement("p");
  desc.className = "preset-desc";
  desc.textContent = currentList.description || "";
  app.appendChild(desc);

  if (Object.keys(weights).length) app.appendChild(sliderSection());
  app.appendChild(renderRanking());
}

async function renderDetail(workId) {
  const data = await api(`/api/v1/works/${encodeURIComponent(workId)}`);
  const dims = data.dims || {};
  const rows = DIM_ORDER.filter((d) => d in dims).map((dim) => {
    const d = dims[dim];
    // unknown 一律显示「—」:那个 0 是占位符,不是得分。把它印成 0.0 会让访客
    // 以为这一维评过分且垫底。
    const cell = (v) => (d.state === "unknown" ? "—" : fmt(v));
    return `<tr><td>${esc(DIM_LABEL[dim] || dim)}</td><td><b>${cell(d.score)}</b></td>
      <td>${cell(d.pct)}</td><td>${cell(d.raw)}</td>
      <td><span class="state">${esc(STATE_LABEL[d.state] || d.state)}</span></td>
      <td>${esc(d.source || "")}</td></tr>`;
  }).join("");

  app.innerHTML = `
    <div class="detail-head">
      <h1>${esc(data.title)} ${gradeBadge(data.grade)}</h1>
      <div class="sub">${esc(data.author || "")}${data.topic ? " · " + esc(data.topic) : ""}${data.level ? " · " + esc(data.level) : ""}</div>
      ${data.reading && data.reading.has_reading ? `<div class="sub">${readingBadges(data)}</div>` : ""}
    </div>
    <h2 class="sec">得分拆解（standard_version ${esc(data.standard_version)}）</h2>
    <table class="dims-table">
      <thead><tr><th>维度</th><th>得分</th><th>百分位</th><th>原始值</th><th>状态</th><th>数据来源</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    ${(data.missing || []).length ? `<div class="missing-box warn"><b>数据不足</b>：缺少 ${
      data.missing.map((m) => esc(DIM_LABEL[m.dim] || m.dim) + "（" + esc(m.why) + "）").join("、")
    }。这几维不参与需要它们的榜单,也不会被当成 0 分计入。</div>` : ""}
    <h2 class="sec">本库持有的版次</h2>
    <div class="editions">${(data.editions || []).map((e) => `
      <div>${esc(e.title)} · ${esc(e.format || "")} · ${esc(e.language || "")} · ${esc(e.publisher || "")} · ${esc(e.pubdate || "")}${
        e.pubdate_source ? "（来源 " + esc(e.pubdate_source) + "）" : ""
      }${e.personal_rating ? " · ★ " + esc(e.personal_rating) : ""}</div>`).join("") || "无"}</div>
    <p class="sub">封面与阅读入口一律外链，本站不保存正文。<br>
      <a href="${esc(data.links.google_books)}" target="_blank" rel="noopener">Google Books</a> ·
      <a href="${esc(data.links.openlibrary)}" target="_blank" rel="noopener">OpenLibrary</a></p>`;
}

/* ---- 全库目录:两千行纯列表没法用,筛选全在客户端(数据已经全在手上) ---- */

let catalog = null;
const catFilter = { q: "", topic: "", level: "", missingOnly: false };

function catalogMatches() {
  const q = catFilter.q.trim().toLowerCase();
  return (catalog.works || []).filter((w) => {
    if (catFilter.topic && w.topic !== catFilter.topic) return false;
    if (catFilter.level && w.level !== catFilter.level) return false;
    if (catFilter.missingOnly && !(w.missing || []).length) return false;
    if (!q) return true;
    return `${w.title} ${w.author || ""}`.toLowerCase().includes(q);
  });
}

function catalogRowsHTML(rows) {
  if (!rows.length) return `<p class="loading">没有匹配的书。</p>`;
  return `<ul class="catalog-grid">${rows.map((w) => `
    <li class="cat-row">
      <span class="cat-title"><a href="#/book/${encodeURIComponent(w.work_id)}">${esc(w.title)}</a></span>
      ${gradeBadge(w.grade)}
      ${(w.missing || []).length ? `<span class="insufficient">数据不足（缺 ${
        w.missing.map((d) => esc(DIM_LABEL[d] || d)).join("、")
      }）</span>` : ""}
      <span class="cat-meta">${esc(w.author || "")}${w.year ? " · " + w.year : ""}${w.topic ? " · " + esc(w.topic) : ""}</span>
    </li>`).join("")}</ul>`;
}

function options(values, selected) {
  return values.map((v) => `<option value="${esc(v)}"${v === selected ? " selected" : ""}>${esc(v)}</option>`).join("");
}

async function renderCatalog() {
  catalog = await api("/api/v1/catalog");
  const uniq = (key) => [...new Set((catalog.works || []).map((w) => w[key]).filter(Boolean))].sort();

  app.innerHTML = `<h1>全库目录（${catalog.total}）</h1>
    <p class="preset-desc">目录收录全库。缺少关键维度的书会标注「数据不足」——
      它进不了需要那几维的榜单,但不会从站上消失。</p>
    <div class="cat-filters">
      <input id="cat-q" type="search" placeholder="搜索书名或作者…" value="${esc(catFilter.q)}">
      <select id="cat-topic"><option value="">全部主题</option>${options(uniq("topic"), catFilter.topic)}</select>
      <select id="cat-level"><option value="">全部层级</option>${options(uniq("level"), catFilter.level)}</select>
      <label class="cat-check"><input id="cat-missing" type="checkbox"${catFilter.missingOnly ? " checked" : ""}> 只看数据不足</label>
      <span id="cat-count" class="coverage"></span>
    </div>
    <div id="cat-list"></div>`;

  const repaint = () => {
    const rows = catalogMatches();
    app.querySelector("#cat-list").innerHTML = catalogRowsHTML(rows);
    app.querySelector("#cat-count").textContent = `${rows.length} / ${catalog.total}`;
  };
  const bind = (sel, ev, fn) => {
    const el = app.querySelector(sel);
    if (el) el.addEventListener(ev, (e) => { fn(e.target); repaint(); });
  };
  bind("#cat-q", "input", (t) => { catFilter.q = t.value; });
  bind("#cat-topic", "change", (t) => { catFilter.topic = t.value; });
  bind("#cat-level", "change", (t) => { catFilter.level = t.value; });
  bind("#cat-missing", "change", (t) => { catFilter.missingOnly = !!t.checked; });
  repaint();
}

async function route() {
  const hash = location.hash || "#/";
  try {
    // 必须 await:直接 return 一个 promise 会让它的 rejection 逃出 catch,
    // 页面就永远停在"加载中"。
    if (hash.startsWith("#/book/")) return await renderDetail(decodeURIComponent(hash.slice(7)));
    if (hash.startsWith("#/list/")) return await renderHome(decodeURIComponent(hash.slice(7)));
    if (hash.startsWith("#/catalog")) return await renderCatalog();
    return await renderHome();
  } catch (e) {
    app.innerHTML = `<p class="loading">出错了：${esc(e.message)}。若是首次启动，请先运行
      <code>readlist seed &amp;&amp; readlist score</code>。</p>`;
  }
}

(async function init() {
  try {
    meta = await api("/api/v1/meta");
    lists = (await api("/api/v1/lists")).lists || [];
  } catch (e) {
    app.innerHTML = `<p class="loading">无法连接 API：${esc(e.message)}</p>`;
    return;
  }
  window.addEventListener("hashchange", route);
  route();
})();
