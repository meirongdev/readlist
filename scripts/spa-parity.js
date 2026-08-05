#!/usr/bin/env node
// 校验内嵌 SPA 的客户端重排与后端评分口径一致。
//
// 为什么需要它:NFR-2 要求权重滑块在浏览器内重排、零网络往返,也就是说
// score.Combine 的公式在 app.js 里有第二份实现。两份实现必须逐位一致,而这件事
// 无法靠 Go 测试覆盖 —— 之前 /api/v1/lists 不返回 weights,SPA 的权重表恒为空,
// 于是每本书都被显示成「0.0 分 · 覆盖 0%」,而 e2e 只 grep 了首页 HTML,没发现。
//
// 用法: BASE=http://127.0.0.1:8080 node scripts/spa-parity.js

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const BASE = (process.env.BASE || 'http://127.0.0.1:8080').replace(/\/$/, '');
const APP_JS = path.join(__dirname, '..', 'internal', 'api', 'dist', 'app.js');
const TOL = 1e-9;

let failures = 0;
const fail = (msg) => { console.log(`FAIL ${msg}`); failures++; };
const ok = (msg) => console.log(`ok   ${msg}`);

// 最小 DOM stub:只为了让 app.js 能被求值,不渲染任何东西。
const elStub = () => ({
  innerHTML: '', className: '', textContent: '', value: '', style: {},
  dataset: {}, onclick: null, parentElement: null,
  appendChild() {}, addEventListener() {}, replaceWith() {},
  querySelector: () => elStub(),
  querySelectorAll: () => [],
});

async function main() {
  const ctx = {
    console,
    document: { querySelector: () => elStub(), createElement: () => elStub() },
    window: { addEventListener() {} },
    location: { hash: '#/unrouted' },
    // 让 app.js 打真实后端 —— 校验的就是真实响应能否驱动它。
    fetch: (p) => fetch(BASE + p),
  };
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(APP_JS, 'utf8'), ctx, { filename: 'app.js' });

  const meta = await (await fetch(`${BASE}/api/v1/meta`)).json();
  if (!meta.run_id) return fail('/api/v1/meta 没有 run_id');
  const { lists } = await (await fetch(`${BASE}/api/v1/lists`)).json();
  if (!lists || !lists.length) return fail('/api/v1/lists 为空');

  let checked = 0;
  for (const l of lists) {
    // 口径字段是滑块的输入契约。
    if (!l.weights || !Object.keys(l.weights).length) { fail(`${l.id}: 响应缺 weights`); continue; }
    if (l.order !== 'desc' && l.order !== 'asc') fail(`${l.id}: order=${l.order}`);
    const sum = Object.values(l.weights).reduce((a, b) => a + b, 0);
    if (Math.abs(sum - 1) > 1e-6) fail(`${l.id}: 权重和 ${sum} ≠ 1`);
    for (const dim of Object.keys(l.bands || {})) {
      if (!(dim in l.weights)) fail(`${l.id}: band 维度 ${dim} 没有权重 → band 是空操作`);
    }

    const payload = await (await fetch(`${BASE}/api/v1/lists/${encodeURIComponent(l.id)}`)).json();
    if (!payload.items || !payload.items.length) { fail(`${l.id}: 榜单为空`); continue; }
    if (!payload.list || !payload.list.weights) { fail(`${l.id}: 单榜响应缺 list 口径`); continue; }

    ctx.__payload = payload;
    const res = vm.runInContext(`(() => {
      currentList = __payload.list;
      currentItems = __payload.items;
      weights = clone(currentList.weights);
      const r = rankedItems();
      return {
        hidden: r.hidden,
        rows: r.kept.map((it, i) => ({ pos: i + 1, id: it.work_id, tbs: it.tbs, cov: it.coverage })),
      };
    })()`, ctx);

    if (res.hidden !== 0) fail(`${l.id}: 默认权重下有 ${res.hidden} 本被 coverage 门槛隐去`);
    const byID = new Map(payload.items.map((it) => [it.work_id, it]));
    for (const r of res.rows) {
      checked++;
      const server = byID.get(r.id);
      if (Math.abs(r.tbs - server.tbs) > TOL) {
        fail(`${l.id}/${r.id}: 客户端 TBS ${r.tbs} ≠ 后端 ${server.tbs}`);
      }
      if (Math.abs(r.cov - server.coverage) > TOL) {
        fail(`${l.id}/${r.id}: 客户端 coverage ${r.cov} ≠ 后端 ${server.coverage}`);
      }
      if (r.pos !== server.rank) fail(`${l.id}/${r.id}: 默认权重下位次 ${r.pos} ≠ 后端 ${server.rank}`);
      if (!(r.tbs > 0)) fail(`${l.id}/${r.id}: TBS 非正 (${r.tbs}) —— 滑块坏掉时的典型症状`);
    }
    ok(`${l.id}: ${res.rows.length} 本,客户端点积与后端逐位一致`);
  }

  // 滑块必须真的能改变排序,否则它只是装饰。
  const moved = vm.runInContext(`(() => {
    weights = clone(currentList.weights);
    const before = rankedItems().kept.map((x) => x.work_id).join(',');
    for (const d in weights) weights[d] = 0;
    weights[Object.keys(currentList.weights)[0]] = 1;
    return before !== rankedItems().kept.map((x) => x.work_id).join(',');
  })()`, ctx);
  moved ? ok('拖动权重改变排序') : fail('把权重压到单一维度后排序不变 —— 滑块没生效');

  // band 目标带与 unknown 的处理必须与后端一致:偏离目标扣分,unknown 不参与。
  const bandOK = vm.runInContext(`(() => {
    const b = { D: { target: 35, tol: 25 } };
    return effectiveScore('D', { state: 'measured', score: 35 }, b) === 100
        && effectiveScore('D', { state: 'measured', score: 95 }, b) === 0
        && effectiveScore('D', { state: 'unknown',  score: 0 },  b) === null;
  })()`, ctx);
  bandOK ? ok('band 生效,unknown 维度不计分') : fail('band 或 unknown 处理与后端不一致');

  console.log(`\n共校验 ${checked} 本;${failures === 0 ? '全部通过' : failures + ' 处失败'}`);
}

main().then(
  () => process.exit(failures === 0 ? 0 : 1),
  (err) => { console.log(`FAIL 未捕获异常: ${err.message}`); process.exit(1); },
);
