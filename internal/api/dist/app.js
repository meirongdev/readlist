/* readlist 内嵌 SPA —— 零依赖,hash 路由,权重滑块纯客户端重排 */
const $ = (sel) => document.querySelector(sel);
const app = $("#app");

const DIM_LABEL = { A: "口碑", C: "技术圈声量", F: "时效", T: "权威", D: "深度", P: "可操作", readability: "馆藏可读性" };
const STATE_LABEL = { measured: "实测", shrunk: "收缩", unknown: "未知" };

let meta = null, lists = [], currentList = null, currentItems = [];
let weights = {}, order = "desc";

async function api(path) {
  const r = await fetch(path);
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.status);
  return r.json();
}

function esc(s) { return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])); }
function fmt(n) { return n == null ? "—" : Number(n).toFixed(1); }

function gradeBadge(g) { return g ? `<span class="badge badge-${g}">${g} 级</span>` : ""; }
function readingBadges(item) {
  if (!item.reading || !item.reading.has_reading) return "";
  const map = { read: ["badge-read", "✓ 已读"], reading: ["badge-reading", "◐ 在读"], unread: [] };
  let s = "";
  if (map[item.reading.status]) s += `<span class="badge ${map[item.reading.status][0]}">${map[item.reading.status][1]}</span>`;
  (item.reading.shelves || []).forEach((sh) => {
    if (sh === "弃读") return; // 弃读默认不公开
    s += `<span class="badge badge-shelf">☆ ${esc(sh)}</span>`;
  });
  return s;
}

/* ---- 权重滑块:纯客户端点积 + band + coverage(与后端公式一致) ---- */
function effectiveScore(dim, d, bands) {
  if (!d || d.state === "unknown") return null;
  if (bands && bands[dim]) { const b = bands[dim]; const v = 100 * (1 - Math.abs(d.score - b.target) / b.tol); return Math.max(0, v); }
  return d.score;
}
function rescore(item, bands) {
  let totalW = 0, availW = 0, acc = 0;
  for (const dim in weights) totalW += weights[dim] || 0;
  for (const dim in weights) {
    const w = weights[dim] || 0;
    const d = item.dims[dim];
    const eff = effectiveScore(dim, d, bands);
    if (eff == null) continue;
    availW += w; acc += w * eff;
  }
  const coverage = totalW > 0 ? availW / totalW : 0;
  const tbs = coverage > 0 ? acc / coverage : 0;
  return { tbs, coverage };
}
function renderList() {
  const bands = (currentList && currentList.bands) || {};
  const items = currentItems.map((it) => Object.assign({}, it, rescore(it, bands)));
  items.sort((a, b) => (order === "asc" ? a.tbs - b.tbs : b.tbs - a.tbs));
  const ol = document.createElement("ol");
  ol.className = "ranking";
  for (const it of items) {
    const li = document.createElement("li");
    li.className = "book";
    li.innerHTML = `
      <div class="book-top">
        <span class="rank-no">${it.rank}</span>
        <span class="book-title"><a href="#/book/${encodeURIComponent(it.work_id)}">${esc(it.title)}</a></span>
        ${gradeBadge(it.grade)}${readingBadges(it)}
        <span class="tbs">${fmt(it.tbs)}<small> 分</small></span>
      </div>
      <div class="book-meta">${esc(it.author || "")}${it.year ? " · " + it.year : ""}${it.topic ? " · " + esc(it.topic) : ""}</div>
      <div class="reason">${it.reason ? it.reason.replace(/^/, "<b>为什么：</b>") : ""}</div>
      <div class="coverage">覆盖 ${Math.round(it.coverage * 100)}%</div>`;
    ol.appendChild(li);
  }
  return ol;
}
function sliderSection() {
  const el = document.createElement("div");
  el.className = "sliders";
  const h = document.createElement("h3");
  h.textContent = "权重滑块（纯客户端即时重排）";
  el.appendChild(h);
  const defaults = JSON.parse(JSON.stringify(weights));
  for (const dim in defaults) {
    const row = document.createElement("div");
    row.className = "slider-row";
    row.innerHTML = `<label>${DIM_LABEL[dim] || dim}</label>
      <input type="range" min="0" max="1" step="0.05" value="${defaults[dim]}" data-dim="${dim}">
      <span class="val">${defaults[dim].toFixed(2)}</span>`;
    el.appendChild(row);
  }
  const reset = document.createElement("button");
  reset.className = "reset";
  reset.textContent = "重置";
  reset.onclick = () => { weights = JSON.parse(JSON.stringify(defaults)); renderPage(); };
  el.appendChild(reset);
  el.addEventListener("input", (ev) => {
    if (ev.target.dataset.dim) {
      const dim = ev.target.dataset.dim;
      weights[dim] = parseFloat(ev.target.value);
      ev.target.parentElement.querySelector(".val").textContent = weights[dim].toFixed(2);
      const ol = renderList();
      const old = app.querySelector("ol.ranking");
      if (old) old.replaceWith(ol);
    }
  });
  return el;
}

async function renderHome(presetId) {
  const id = presetId || (lists[0] && lists[0].id) || "timeless";
  currentList = lists.find((l) => l.id === id) || lists[0];
  const data = await api(`/api/v1/lists/${encodeURIComponent(currentList.id)}`);
  currentItems = data.items || [];
  weights = JSON.parse(JSON.stringify(currentList.weights || {}));
  order = currentList.order || "desc";
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
  app.appendChild(sliderSection());
  app.appendChild(renderList());
}

async function renderDetail(workId) {
  const data = await api(`/api/v1/works/${encodeURIComponent(workId)}`);
  app.innerHTML = `
    <div class="detail-head">
      <h1>${esc(data.title)} ${gradeBadge(data.grade)}</h1>
      <div class="sub">${esc(data.author || "")}${data.topic ? " · " + esc(data.topic) : ""}${data.level ? " · " + esc(data.level) : ""}</div>
      ${data.reading && data.reading.has_reading ? `<div class="sub">${readingBadges(data)}</div>` : ""}
    </div>
    <h2 class="sec">得分拆解（standard_version ${esc(data.standard_version)}）</h2>
    <table class="dims-table">
      <thead><tr><th>维度</th><th>得分</th><th>百分位</th><th>原始值</th><th>状态</th><th>数据来源</th></tr></thead>
      <tbody>${Object.entries(data.dims).map(([dim, d]) => `
        <tr><td>${DIM_LABEL[dim] || dim}</td><td><b>${fmt(d.score)}</b></td>
        <td>${fmt(d.pct)}</td><td>${fmt(d.raw)}</td>
        <td><span class="state">${STATE_LABEL[d.state] || d.state}</span></td>
        <td>${esc(d.source || "")}</td></tr>`).join("")}
      </tbody>
    </table>
    ${data.missing && data.missing.length ? `<div class="missing-box warn"><b>数据不足</b>：缺少 ${data.missing.map((m) => DIM_LABEL[m.dim] + "（" + m.why + "）").join("、")}</div>` : ""}
    <h2 class="sec">本库持有的版次</h2>
    <div class="editions">${(data.editions || []).map((e) => `
      <div>${esc(e.title)} · ${esc(e.format || "")} · ${esc(e.language || "")} · ${esc(e.publisher || "")} · ${esc(e.pubdate || "")}${e.personal_rating ? " · ★ " + e.personal_rating : ""}</div>`).join("") || "无"}</div>
    <p class="sub">封面与阅读入口一律外链，本站不保存正文。<br>
      <a href="${esc(data.links.google_books)}" target="_blank" rel="noopener">Google Books</a> ·
      <a href="${esc(data.links.openlibrary)}" target="_blank" rel="noopener">OpenLibrary</a></p>`;
}

async function renderCatalog() {
  const data = await api("/api/v1/catalog");
  app.innerHTML = `<h1>全库目录（${data.total}）</h1>
    <p class="preset-desc">A/B 级可上榜；C 级仅在此目录页展示并标注「数据不足」。</p>
    <ul class="catalog-grid">${data.works.map((w) => `
      <li class="cat-row">
        <span class="cat-title"><a href="#/book/${encodeURIComponent(w.work_id)}">${esc(w.title)}</a></span>
        ${gradeBadge(w.grade)}
        ${w.grade === "C" ? '<span class="insufficient">数据不足</span>' : ""}
        <span class="cat-meta">${esc(w.author || "")}${w.year ? " · " + w.year : ""}${w.topic ? " · " + esc(w.topic) : ""}</span>
      </li>`).join("")}</ul>`;
}

async function route() {
  const hash = location.hash || "#/";
  try {
    if (hash.startsWith("#/book/")) return renderDetail(decodeURIComponent(hash.slice(7)));
    if (hash.startsWith("#/list/")) return renderHome(decodeURIComponent(hash.slice(7)));
    if (hash.startsWith("#/catalog")) return renderCatalog();
    return renderHome();
  } catch (e) {
    app.innerHTML = `<p class="loading">出错了：${esc(e.message)}。请先运行 <code>readlist seed && readlist score</code>。</p>`;
  }
}

(async function init() {
  try {
    meta = await api("/api/v1/meta");
    const ldata = await api("/api/v1/lists");
    lists = ldata.lists || [];
  } catch (e) {
    app.innerHTML = `<p class="loading">无法连接 API：${esc(e.message)}</p>`;
    return;
  }
  window.addEventListener("hashchange", route);
  route();
})();
