/* readlist 内嵌 SPA —— 零依赖,hash 路由,纯展示 */
const $ = (sel) => document.querySelector(sel);
const app = $("#app");

const DIM_ORDER = ["A", "C", "F", "T", "D", "P", "readability"];
const DIM_LABEL = { A: "口碑", C: "技术圈声量", F: "时效", T: "权威", D: "深度", P: "可操作", readability: "馆藏可读性" };
const STATE_LABEL = { measured: "实测", shrunk: "收缩", unknown: "未知" };

let lists = [];
let currentList = null;

async function api(path) {
  const r = await fetch(path);
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `HTTP ${r.status}`);
  return r.json();
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
function fmt(n) { return n == null ? "—" : Number(n).toFixed(1); }

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

/* 榜单的口径声明。「榜单 = 权重档案」是本站的核心主张,所以权重必须写在榜单页上,
   而不是藏在 API 里。这里只读展示 —— 让访客在客户端重调权重意味着把 score.Combine
   用 JS 再实现一遍,两份公式靠一条 parity 测试对齐;而它能做的也只是在**已经选出的
   这 20 本**里换个顺序,选不进榜外的书(那需要重跑选材:去重 + 多样性约束)。
   为这点收益养一套双实现不划算,所以撤掉了。 */
function weightsLine(list) {
  const w = list.weights || {};
  const parts = DIM_ORDER.filter((d) => d in w)
    .map((d) => `${esc(DIM_LABEL[d] || d)} ${Math.round(w[d] * 100)}%`);
  if (!parts.length) return "";
  return `<p class="weights">口径：${parts.join(" · ")}</p>`;
}

function renderRanking(items) {
  if (!items.length) {
    // 榜为空只可能是证据还没到位(needs 是逐维硬门),不是排序问题 —— 别让访客
    // 以为是自己哪里没点对。
    return `<p class="loading">这份榜暂时没有书:入选要求的证据维度还没有采集到足够的数据。</p>`;
  }
  return `<ol class="ranking">${items.map((it) => `
    <li class="book">
      <div class="book-top">
        <span class="rank-no">${it.rank}</span>
        <span class="book-title"><a href="#/book/${encodeURIComponent(it.work_id)}">${esc(it.title)}</a></span>
        ${gradeBadge(it.grade)}${readingBadges(it)}
        <span class="tbs">${fmt(it.tbs)}<small> 分</small></span>
      </div>
      <div class="book-meta">${esc(it.author || "")}${it.year ? " · " + it.year : ""}${it.topic ? " · " + esc(it.topic) : ""}</div>
      <div class="reason">${it.reason ? "<b>为什么：</b>" + esc(it.reason) : ""}</div>
      <div class="coverage">覆盖 ${Math.round(it.coverage * 100)}%</div>
    </li>`).join("")}</ol>`;
}

async function renderHome(presetId) {
  const id = presetId || (lists[0] && lists[0].id);
  if (!id) throw new Error("没有可用的榜单");
  const data = await api(`/api/v1/lists/${encodeURIComponent(id)}`);
  // 口径以单榜响应里的 list 为准。
  currentList = data.list || lists.find((l) => l.id === id) || {};

  const tabs = lists.map((l) =>
    `<button data-id="${esc(l.id)}"${l.id === currentList.id ? ' class="active"' : ""}>${esc(l.name)}</button>`
  ).join("");

  app.innerHTML = `<div class="preset-bar">${tabs}</div>
    <p class="preset-desc">${esc(currentList.description || "")}</p>
    ${weightsLine(currentList)}
    ${renderRanking(data.items || [])}`;

  app.querySelector(".preset-bar").addEventListener("click", (ev) => {
    const id = ev.target.dataset && ev.target.dataset.id;
    if (id) location.hash = `#/list/${id}`;
  });
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

/* ---- 上榜书目:各榜并集的一张总表,筛选全在客户端(数据已经全在手上)。
       注意这里不是全库目录 —— 没上榜的藏书不进公开面。 ---- */

let catalog = null;
const catFilter = { q: "", topic: "" };

function catalogMatches() {
  const q = catFilter.q.trim().toLowerCase();
  return (catalog.works || []).filter((w) => {
    if (catFilter.topic && w.topic !== catFilter.topic) return false;
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
      <span class="cat-meta">${esc(w.author || "")}${w.year ? " · " + w.year : ""}${w.topic ? " · " + esc(w.topic) : ""}</span>
    </li>`).join("")}</ul>`;
}

async function renderCatalog() {
  catalog = await api("/api/v1/catalog");
  const topics = [...new Set((catalog.works || []).map((w) => w.topic).filter(Boolean))].sort();

  app.innerHTML = `<h1>上榜书目（${catalog.total}）</h1>
    <p class="preset-desc">这里是各份榜单收录的书去重后的总表,不是全库目录 ——
      没上榜的藏书不在公开面上。</p>
    <div class="cat-filters">
      <input id="cat-q" type="search" placeholder="搜索书名或作者…" value="${esc(catFilter.q)}">
      <select id="cat-topic"><option value="">全部主题</option>${
        topics.map((t) => `<option value="${esc(t)}"${t === catFilter.topic ? " selected" : ""}>${esc(t)}</option>`).join("")
      }</select>
      <span id="cat-count" class="coverage"></span>
    </div>
    <div id="cat-list"></div>`;

  const repaint = () => {
    const rows = catalogMatches();
    app.querySelector("#cat-list").innerHTML = catalogRowsHTML(rows);
    app.querySelector("#cat-count").textContent = `${rows.length} / ${catalog.total}`;
  };
  app.querySelector("#cat-q").addEventListener("input", (e) => { catFilter.q = e.target.value; repaint(); });
  app.querySelector("#cat-topic").addEventListener("change", (e) => { catFilter.topic = e.target.value; repaint(); });
  repaint();
}

/* ---- 关于:访客落地时唯一的上下文来源。
       没有它,页面就是一堆书名加一个没解释过的数字。 ---- */

function renderAbout() {
  app.innerHTML = `
    <h1>关于这个站</h1>
    <p class="prose">我有一个 2,054 本的 Calibre 技术书库。这里给里面的书打分,挑出几份书单公开出来
      —— 站上<b>只有上过榜的那百来本</b>,全库既不展示也不提供检索。不发书,只发书单和上榜书的元数据。</p>

    <h2 class="sec">书单是什么</h2>
    <p class="prose">每份书单就是<b>一组权重</b>。同一批分数,换一组权重就是另一份榜。
      权重直接印在书单页标题下面 —— 你看到的排名按什么算,页面上写着。</p>

    <h2 class="sec">分数怎么读</h2>
    <p class="prose">书名旁的数字(0–100)是这几个维度的加权平均:</p>
    <table class="dims-table">
      <thead><tr><th>维度</th><th>看的是什么</th><th>数据从哪来</th></tr></thead>
      <tbody>
        <tr><td>口碑</td><td>跨源评分,按评分人数做贝叶斯收缩 —— 3,000 人打的 4.5 分比 5 个人打的 5.0 分更可信</td><td>Google Books / OpenLibrary</td></tr>
        <tr><td>技术圈声量</td><td>Hacker News 上被提到过几次,越久远的提及权重越低</td><td>HN Algolia</td></tr>
        <tr><td>时效</td><td>按主题半衰期衰减 —— 框架书两年半、编译原理二十五年,「三年前出版」的含义完全不同</td><td>可信的出版日期</td></tr>
        <tr><td>权威</td><td>出版社层级 × 作者是否可考</td><td>本地元数据</td></tr>
        <tr><td>馆藏可读性</td><td>格式、封面、简介、ISBN 是否齐全</td><td>本地元数据</td></tr>
      </tbody>
    </table>
    <ul class="prose">
      <li><b>「按 4/4 维评出 · 覆盖 100%」</b> —— 某一维实在没有证据时,它不会被填一个猜出来的数,
        而是被<b>整个移出这本书的权重</b>再重新归一。覆盖率低的书,分数是在更少的维度上算的。</li>
      <li><b>A/B/C/D 徽章</b> —— 只表示证据有多足,<b>不参与任何准入</b>,更不是「书好不好」的评级。</li>
      <li><b>「为什么」那一行</b> —— 上榜理由是算出来的,不是写出来的。人工置顶的书会明说「人工置顶」:
        策展不该伪装成算法结果。</li>
    </ul>
    <p class="prose">分数<b>只在本库内部可比</b>。它衡量的是「在这 2,054 本里,这本书的证据有多强」,
      不是这本书在世界上的绝对排名。</p>

    <h2 class="sec">为什么搜不到某本书</h2>
    <p class="prose">公开面 = <b>各份书单的并集</b>,不是全库目录。「上榜书目」页是这几份榜去重后的
      总表,不是藏书清单。</p>
    <p class="prose">有些书进不了榜是因为<b>缺证据</b>而不是因为差:一本没有 ISBN 的自出版好书拿不到
      外部评分,就进不了要求「口碑可信」的榜。这是诚实,不是筛选。</p>`;
}

async function route() {
  const hash = location.hash || "#/";
  try {
    // 必须 await:直接 return 一个 promise 会让它的 rejection 逃出 catch,
    // 页面就永远停在"加载中"。
    if (hash.startsWith("#/about")) return renderAbout();
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
    lists = (await api("/api/v1/lists")).lists || [];
  } catch (e) {
    app.innerHTML = `<p class="loading">无法连接 API：${esc(e.message)}</p>`;
    return;
  }
  window.addEventListener("hashchange", route);
  route();
})();
