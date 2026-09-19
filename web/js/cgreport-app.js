/* cgreport-app.js —— 由页面内联脚本外置（LiteReport） */
// 报表访问地址 /online/cgreport/{id或code}
var reportKey = decodeURIComponent(location.pathname.split('/').pop() || '');
var code = '';            // 报表编码（公共接口使用）
var fields = [];          // 字段配置
var showFields = [];      // 列显示字段
var sortCol = '', sortDir = '';
var page = 1, total = 0;
var lastRecords = [];

renderNav('AUTO在线报表');
// 分享链接支持：?share=xxx 时所有公共接口请求自动携带 share 参数
var shareToken = new URLSearchParams(location.search).get('share') || '';
var _rawApi = api;
api = function (url, options) {
  if (shareToken && url.indexOf('/online/cgreport/api/') === 0) {
    url += (url.indexOf('?') >= 0 ? '&' : '?') + 'share=' + encodeURIComponent(shareToken);
  }
  return _rawApi(url, options);
};

// 追加 AUTO在线报表 标签
(function () {
  var nav = $('#nav');
  var a = document.createElement('a');
  a.className = 'tab active';
  a.href = '/online/cgreport/' + reportKey;
  a.textContent = 'AUTO在线报表';
  nav.insertBefore(a, nav.querySelector('.spacer'));
})();

$('#apiUrl').textContent = location.origin + '/online/cgreport/api/getData/' + reportKey +
  (shareToken ? '?share=' + encodeURIComponent(shareToken) : '');

// 1. 加载报表配置（getInfo 公共接口）
var dictMaps = {}; // dictCode -> {value: label}
var reportParams = []; // 报表参数
api('/online/cgreport/api/getInfo/' + encodeURIComponent(reportKey))
  .then(function (info) {
    code = info.head.reportCode;
    fields = info.fields || [];
    reportParams = info.params || [];
    if (!fields.length) { fields = (info.columns || []).map(function (c) { return { fieldName: c, fieldTxt: c, fieldType: '字符类型' }; }); }
    showFields = fields.filter(function (f) { return f.isShow !== 'N'; });
    hasTotal = fields.some(function (f) { return f.isTotal === 'Y' && f.fieldType === '数值类型'; });
    document.title = (info.head.reportName || 'AUTO在线报表') + ' - AUTO在线报表';
    // 字典：收集 dict_code（字段+参数同名）并加载（值→标签）
    var codes = [];
    fields.forEach(function (f) { if (f.dictCode && codes.indexOf(f.dictCode) < 0) codes.push(f.dictCode); });
    reportParams.forEach(function (p) { if (p.paramName && codes.indexOf(p.paramName) < 0) codes.push(p.paramName); });
    var jobs = codes.map(function (cd) {
      return api('/online/cgreport/api/getDict/' + encodeURIComponent(cd)).then(function (items) {
        var m = {};
        (items || []).forEach(function (d) { m[d.value] = d.label; });
        dictMaps[cd] = m;
      }).catch(function () {});
    });
    Promise.all(jobs).then(function () { renderQueryArea(); loadData(1); });
    if (!codes.length) { renderQueryArea(); loadData(1); }
  })
  .catch(function (e) {
    $('#tableBody').innerHTML = '<tr><td class="empty-tip">报表加载失败：' + esc(e.message) + '</td></tr>';
    toast(e.message, 'error');
  });

// 2. 查询条件区（isQuery 字段 + 报表参数；字典→下拉；日期between→快捷区间）
var DATE_RANGES = [['', '自定义'], ['d7', '近7天'], ['d30', '近30天'], ['month', '本月'], ['lastmonth', '上月']];
function fmtDate(d) {
  return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
}
function dateRange(key) {
  var now = new Date(), end = fmtDate(now) + ' 23:59:59', start = '';
  if (key === 'd7') { var d = new Date(now - 6 * 864e5); start = fmtDate(d) + ' 00:00:00'; }
  else if (key === 'd30') { var d2 = new Date(now - 29 * 864e5); start = fmtDate(d2) + ' 00:00:00'; }
  else if (key === 'month') { start = fmtDate(new Date(now.getFullYear(), now.getMonth(), 1)) + ' 00:00:00'; }
  else if (key === 'lastmonth') {
    start = fmtDate(new Date(now.getFullYear(), now.getMonth() - 1, 1)) + ' 00:00:00';
    end = fmtDate(new Date(now.getFullYear(), now.getMonth(), 0)) + ' 23:59:59';
  }
  return [start, end];
}
function renderQueryArea() {
  var qf = fields.filter(function (f) { return f.isQuery === 'Y'; });
  var area = $('#queryArea');
  if (!qf.length && !reportParams.length) { area.style.display = 'none'; return; }
  area.style.display = 'flex';
  var html = qf.map(function (f) {
    var id = 'q_' + f.fieldName;
    if (f.queryMode === 'between') {
      var quick = '';
      if (f.fieldType === '日期类型') {
        quick = '<select class="input short" id="' + id + '_quick" onchange="quickRange(\'' + f.fieldName + '\', this.value)">' +
          DATE_RANGES.map(function (r) { return '<option value="' + r[0] + '">' + r[1] + '</option>'; }).join('') + '</select>';
      }
      return '<span class="q-item"><label>' + esc(f.fieldTxt) + ':</label>' + quick +
        '<input class="input short" id="' + id + '_begin" placeholder="开始值"> ~ ' +
        '<input class="input short" id="' + id + '_end" placeholder="结束值"></span>';
    }
    var dm = dictMaps[f.dictCode];
    if (dm) {
      var opts = Object.keys(dm).map(function (v) { return '<option value="' + esc(v) + '">' + esc(dm[v]) + '</option>'; }).join('');
      return '<span class="q-item"><label>' + esc(f.fieldTxt) + ':</label>' +
        '<select class="input short" id="' + id + '"><option value="">全部</option>' + opts + '</select></span>';
    }
    return '<span class="q-item"><label>' + esc(f.fieldTxt) + ':</label>' +
      '<input class="input" id="' + id + '" placeholder="' + (f.queryMode === 'like' ? '模糊查询' : f.queryMode === 'in' ? '多个值用,分隔' : f.queryMode) + '"></span>';
  }).join('');
  // 报表参数（${param}）：有同名字典渲染下拉，否则文本框（占位=默认值）
  html += reportParams.map(function (p) {
    var id = 'p_' + p.paramName;
    var dm = dictMaps[p.paramName];
    if (dm) {
      var opts = Object.keys(dm).map(function (v) { return '<option value="' + esc(v) + '"' + (v === p.paramValue ? ' selected' : '') + '>' + esc(dm[v]) + '</option>'; }).join('');
      return '<span class="q-item"><label>' + esc(p.paramTxt || p.paramName) + ':</label>' +
        '<select class="input short" id="' + id + '"><option value="">(默认)</option>' + opts + '</select></span>';
    }
    return '<span class="q-item"><label>' + esc(p.paramTxt || p.paramName) + ':</label>' +
      '<input class="input short" id="' + id + '" placeholder="' + esc(p.paramValue || p.paramName) + '"></span>';
  }).join('');
  html += '<button class="btn btn-primary btn-sm" onclick="loadData(1)">🔍 查询</button>' +
    '<button class="btn btn-sm" onclick="resetQuery()">↻ 重置</button>';
  area.innerHTML = html;
}
function quickRange(field, key) {
  if (!key) return;
  var r = dateRange(key);
  $('#q_' + field + '_begin').value = r[0];
  $('#q_' + field + '_end').value = r[1];
}
function resetQuery() {
  $$('#queryArea input').forEach(function (i) { i.value = ''; });
  $$('#queryArea select').forEach(function (sl) { sl.selectedIndex = 0; });
  loadData(1);
}
function collectQuery() {
  var q = [];
  fields.filter(function (f) { return f.isQuery === 'Y'; }).forEach(function (f) {
    var v0 = $('#q_' + f.fieldName), v1 = $('#q_' + f.fieldName + '_begin'), v2 = $('#q_' + f.fieldName + '_end');
    if (f.queryMode === 'between') {
      if (v1 && v1.value) q.push(encodeURIComponent(f.fieldName + '_begin') + '=' + encodeURIComponent(v1.value));
      if (v2 && v2.value) q.push(encodeURIComponent(f.fieldName + '_end') + '=' + encodeURIComponent(v2.value));
    } else if (v0 && v0.value) {
      q.push(encodeURIComponent(f.fieldName) + '=' + encodeURIComponent(v0.value));
    }
  });
  // 报表参数（${param} 覆盖默认值）
  reportParams.forEach(function (p) {
    var el = $('#p_' + p.paramName);
    if (el && el.value) q.push(encodeURIComponent(p.paramName) + '=' + encodeURIComponent(el.value));
  });
  return q.join('&');
}

// ═════════ 列上筛选（列头下拉，任意列可筛选，与查询区条件叠加）═════════
var CF_MODES = [['=', '等于'], ['!=', '不等于'], ['like', '包含'], ['in', '在...之中(逗号分隔)'],
  ['bw', '介于(之间)'], ['>', '大于'], ['>=', '大于等于'], ['<', '小于'], ['<=', '小于等于']];
var colFilters = {};  // field -> {mode, v1, v2}
var hasTotal = false; // 报表是否配置了合计列

function openColFilter(ev, field) {
  ev.stopPropagation();
  var panel = $('#cfPanel');
  var cur = colFilters[field] || {};
  var f = showFields.find(function (x) { return x.fieldName === field; }) || {};
  $('#cfTitle').textContent = '筛选: ' + (f.fieldTxt || field);
  panel.dataset.col = field;
  $('#cfMode').innerHTML = CF_MODES.map(function (m) {
    return '<option value="' + m[0] + '"' + ((cur.mode || '=') === m[0] ? ' selected' : '') + '>' + m[0] + ' ' + m[1] + '</option>';
  }).join('');
  $('#cfV1').value = cur.v1 || '';
  $('#cfV2').value = cur.v2 || '';
  cfModeChanged();
  var rect = ev.target.getBoundingClientRect();
  panel.style.top = (rect.bottom + 6) + 'px';
  panel.style.left = Math.min(rect.left - 80, window.innerWidth - 230) + 'px';
  panel.classList.add('show');
  setTimeout(function () { $('#cfV1').focus(); }, 50);
}
function cfModeChanged() {
  $('#cfV2').parentElement.style.display = $('#cfMode').value === 'bw' ? 'flex' : 'none';
  $('#cfV1').placeholder = $('#cfMode').value === 'in' ? '多个值用 , 分隔' : ($('#cfMode').value === 'bw' ? '开始值' : '值');
}
function applyColFilter() {
  var field = $('#cfPanel').dataset.col;
  var v1 = $('#cfV1').value.trim(), v2 = $('#cfV2').value.trim();
  if (!v1) { toast('请输入筛选值', 'error'); return; }
  colFilters[field] = { mode: $('#cfMode').value, v1: v1, v2: v2 };
  closeColFilter();
  renderHead();
  loadData(1);
}
function clearColFilter() {
  var field = $('#cfPanel').dataset.col;
  delete colFilters[field];
  closeColFilter();
  renderHead();
  loadData(1);
}
function closeColFilter() { $('#cfPanel').classList.remove('show'); }
document.addEventListener('click', function (e) {
  if (!e.target.closest('.col-filter-panel') && !e.target.closest('.filter-btn')) closeColFilter();
});
// 列筛选 → 服务端参数
function collectColFilterQuery() {
  return Object.keys(colFilters).map(function (col) {
    var cf = colFilters[col];
    var enc = encodeURIComponent;
    switch (cf.mode) {
      case '=': return enc(col) + '=' + enc(cf.v1);
      case '!=': return enc(col + '_ne') + '=' + enc(cf.v1);
      case '>': return enc(col + '_gt') + '=' + enc(cf.v1);
      case '>=': return enc(col + '_ge') + '=' + enc(cf.v1);
      case '<': return enc(col + '_lt') + '=' + enc(cf.v1);
      case '<=': return enc(col + '_le') + '=' + enc(cf.v1);
      case 'like': return enc(col + '_like') + '=' + enc(cf.v1);
      case 'in': return enc(col + '_in') + '=' + enc(cf.v1);
      case 'bw': return enc(col + '_bw') + '=' + enc(cf.v1 + (cf.v2 ? ',' + cf.v2 : ''));
    }
    return '';
  }).filter(Boolean).join('&');
}
// 数值格式化：去尾零，最多6位小数
function fmtNum(v) {
  if (typeof v === 'number' || (typeof v === 'string' && v !== '' && !isNaN(v))) {
    var n = parseFloat(v);
    if (!isFinite(n)) return String(v);
    return String(parseFloat(n.toFixed(6)));
  }
  return fmtCell(v);
}

// 3. 公共查询接口获取数据
function loadData(p) {
  page = p || page || 1;
  var ps = parseInt(($('#pageSize').value || '10'));
  var qs = ['pageNo=' + page, 'pageSize=' + ps];
  var cq = collectQuery(), cfq = collectColFilterQuery();
  if (cq) qs.push(cq);
  if (cfq) qs.push(cfq);
  if (hasTotal) qs.push('needSummary=true');
  if (sortCol) qs.push('column=' + encodeURIComponent(sortCol), 'order=' + sortDir);
  api('/online/cgreport/api/getData/' + encodeURIComponent(code || reportKey) + '?' + qs.join('&'))
    .then(function (r) {
      lastRecords = r.records || [];
      total = r.total;
      if (!showFields.length && lastRecords.length) {
        showFields = Object.keys(lastRecords[0]).map(function (c) { return { fieldName: c, fieldTxt: c, fieldType: '字符类型' }; });
      }
      renderHead(); renderBody(); renderFoot(r.summary); renderPager();
    })
    .catch(function (e) { toast(e.message, 'error'); });
}

function renderHead() {
  var th = '<th style="width:40px"><input type="checkbox" onchange="toggleAll(this)"></th>';
  th += showFields.map(function (f) {
    var arr = sortCol === f.fieldName
      ? '<span class="arrow on">' + (sortDir === 'desc' ? '▼' : '▲') + '</span>' : '<span class="arrow">⇅</span>';
    var fb = '<span class="filter-btn' + (colFilters[f.fieldName] ? ' on' : '') + '" title="列筛选" ' +
      'onclick="openColFilter(event, \'' + f.fieldName + '\')">▽</span>';
    return '<th><span class="sort" onclick="sortBy(\'' + f.fieldName + '\')">' + esc(f.fieldTxt || f.fieldName) + arr + '</span>' + fb + '</th>';
  }).join('');
  $('#tableHead').innerHTML = '<tr>' + th + '</tr>';
}
// 合计行：服务端对合计列配置的数值字段求 SUM（全量结果集）
function renderFoot(summary) {
  var foot = $('#tableFoot');
  if (!summary || !Object.keys(summary).length) { foot.innerHTML = ''; return; }
  var tds = '<td class="total-label">合计</td>' + showFields.map(function (f) {
    var v = summary[f.fieldName];
    return '<td>' + (v !== undefined && v !== null ? esc(fmtNum(v)) : '') + '</td>';
  }).join('');
  foot.innerHTML = '<tr class="total-row">' + tds + '</tr>';
}
function sortBy(col) {
  if (sortCol === col) sortDir = sortDir === 'asc' ? 'desc' : 'asc';
  else { sortCol = col; sortDir = 'asc'; }
  loadData(1);
}
function renderBody() {
  var tbody = $('#tableBody');
  if (!lastRecords.length) {
    tbody.innerHTML = '<tr><td colspan="' + (showFields.length + 1) + '" class="empty-tip">暂无数据</td></tr>';
    return;
  }
  var cellOf = function (f, rec) {
    var raw = fmtCell(rec[f.fieldName]);
    var dm = f.dictCode && dictMaps[f.dictCode];
    if (dm && dm[raw] !== undefined) return dm[raw];
    return raw;
  };
  // 渲染规则：{"lt":0} 小于标红；{"gt":100} 大于标绿（数值字段）
  var ruleColor = function (f, v) {
    if (!f.renderRule || v === '' || isNaN(v)) return '';
    try {
      var r = JSON.parse(f.renderRule), n = parseFloat(v);
      if (r.lt !== undefined && n < r.lt) return 'color:#ff4d4f;font-weight:600';
      if (r.gt !== undefined && n > r.gt) return 'color:#52c41a;font-weight:600';
    } catch (e) {}
    return '';
  };
  // 渲染规则：{"img":true} 把单元格值作为图片 URL 渲染
  var isImgField = function (f) {
    if (!f.renderRule) return false;
    try { return JSON.parse(f.renderRule).img === true; } catch (e) { return false; }
  };
  // fieldHref 链接模板：支持 {value} 与 {id} 占位
  var hrefOf = function (f, rec, display) {
    if (!f.fieldHref) return null;
    var url = f.fieldHref
      .replace(/\{value\}/g, encodeURIComponent(display))
      .replace(/\{id\}/g, encodeURIComponent(rec.id || ''));
    return url;
  };
  tbody.innerHTML = lastRecords.map(function (rec, idx) {
    var id = rec.id !== undefined && rec.id !== null ? String(rec.id) : '';
    var tds = '<td><input type="checkbox" class="row-check" data-idx="' + idx + '" onchange="selChanged()"></td>';
    tds += showFields.map(function (f) {
      var disp = cellOf(f, rec);
      var style = ruleColor(f, disp);
      var inner = esc(disp);
      if (isImgField(f) && disp) {
        inner = '<img src="' + esc(disp) + '" style="max-height:42px;border-radius:4px;vertical-align:middle" alt="">';
      }
      var href = hrefOf(f, rec, disp);
      if (href) inner = '<a href="' + esc(href) + '" target="_blank" style="text-decoration:underline">' + inner + '</a>';
      return '<td title="' + esc(disp) + '"' + (style ? ' style="' + style + '"' : '') + '>' + inner + '</td>';
    }).join('');
    return '<tr data-id="' + esc(id) + '" ondblclick="openSave(' + idx + ')">' + tds + '</tr>';
  }).join('');
}
function renderPager() {
  $('#total').textContent = total;
  var ps = parseInt(($('#pageSize').value || '10')), pages = Math.max(1, Math.ceil(total / ps)), cur = page;
  var html = '<span class="pg' + (cur <= 1 ? ' disabled' : '') + '" onclick="loadData(' + (cur - 1) + ')">&lt;</span>';
  for (var i = 1; i <= pages; i++) {
    if (pages > 7 && i > 2 && i < pages - 1 && Math.abs(i - cur) > 1) {
      if (html.indexOf('…') < 0) html += '<span class="pg disabled">…</span>';
      continue;
    }
    html += '<span class="pg' + (i === cur ? ' cur' : '') + '" onclick="loadData(' + i + ')">' + i + '</span>';
  }
  html += '<span class="pg' + (cur >= pages ? ' disabled' : '') + '" onclick="loadData(' + (cur + 1) + ')">&gt;</span>';
  $('#pages').innerHTML = html;
}
function toggleAll(cb) { $$('.row-check').forEach(function (c) { c.checked = cb.checked; }); selChanged(); }
function selChanged() {
  var n = $$('.row-check:checked').length;
  $('#selTip').textContent = n > 0 ? '已选中 ' + n + ' 条数据' : '未选中任何数据';
}
function selectedIdx() {
  var cks = $$('.row-check:checked');
  return cks.length ? cks.map(function (c) { return +c.dataset.idx; }) : [];
}

/* ───── 公共保存接口演示 ───── */
function openSave(idx) {
  var rec = idx === null ? { id: '' } : Object.assign({}, lastRecords[idx]);
  if (idx === null) {
    showFields.forEach(function (f) { if (!(f.fieldName in rec)) rec[f.fieldName] = null; });
    rec.id = '';
  }
  $('#saveTitle').textContent = idx === null ? '新增数据（公共保存接口）' : '修改数据（公共保存接口）';
  $('#saveApi').textContent = 'POST /online/cgreport/api/saveData/' + code;
  $('#saveJson').value = JSON.stringify({ data: rec }, null, 2);
  openModal('saveModal');
}
function openSaveSelected() {
  var idxs = selectedIdx();
  if (idxs.length !== 1) { toast('请勾选一条要修改的数据（或双击行）', 'error'); return; }
  openSave(idxs[0]);
}
function doSave() {
  var body;
  try { body = JSON.parse($('#saveJson').value); } catch (e) { toast('JSON格式错误：' + e.message, 'error'); return; }
  api('/online/cgreport/api/saveData/' + encodeURIComponent(code), { method: 'POST', body: body })
    .then(function (r) { toast('保存成功（' + r.saved + '条）', 'success'); closeModal('saveModal'); loadData(); })
    .catch(function (e) { toast(e.message, 'error'); });
}
function delSelected() {
  var idxs = selectedIdx();
  if (!idxs.length) { toast('请勾选要删除的数据', 'error'); return; }
  var ids = idxs.map(function (i) { return String(lastRecords[i].id); });
  if (ids.some(function (x) { return !x || x === 'undefined'; })) { toast('数据缺少 id 主键，无法删除', 'error'); return; }
  if (!confirm('确认删除选中的 ' + ids.length + ' 条数据吗？')) return;
  api('/online/cgreport/api/deleteData/' + encodeURIComponent(code), { method: 'POST', body: { ids: ids } })
    .then(function (r) { toast('删除成功（' + r.deleted + '条）', 'success'); loadData(); })
    .catch(function (e) { toast(e.message, 'error'); });
}

/* ───── 导出 Excel（服务端生成，含样式与合计行）───── */
function exportExcel() {
  var m = $('#exportMenu'); if (m) m.style.display = 'none';
  var base = collectQuery();
  var cfq = collectColFilterQuery();
  if (cfq) base += cfq;
  // 先查总数：超过 10 万行导出上限时明确告知（避免静默截断）
  api('/online/cgreport/api/getData/' + encodeURIComponent(code) + '?pageNo=1&pageSize=1&needSummary=false&' + base)
    .then(function (r) {
      var total = r.total;
      if (total > 100000 && !confirm('当前筛选共 ' + total + ' 行，超过单次导出上限 100000 行，将仅导出前 100000 行。继续导出？')) return;
      var qs = 'needCount=false&' + (hasTotal ? 'needSummary=true&' : '') + base;
      window.open('/online/cgreport/api/exportExcel/' + encodeURIComponent(code) + '?' + qs + (shareToken ? '&share=' + encodeURIComponent(shareToken) : ''));
    })
    .catch(function (e) { toast(e.message, 'error'); });
}

/* 导出下拉菜单开关 */
function toggleExport() {
  var menu = $('#exportMenu');
  if (menu) menu.style.display = menu.style.display === 'none' ? '' : 'none';
}
document.addEventListener('click', function (e) {
  var menu = $('#exportMenu');
  if (menu && !e.target.closest('.btn-group')) menu.style.display = 'none';
});

/* ───── 导出 JSON / SQL Insert（服务端生成，走当前查询条件）───── */
function exportJson() {
  var qs = collectQuery();
  var cfq = collectColFilterQuery();
  if (cfq) qs += cfq;
  window.open('/online/cgreport/api/exportJson/' + encodeURIComponent(code) + '?' + qs + (shareToken ? '&share=' + encodeURIComponent(shareToken) : ''));
  $('#exportMenu').style.display = 'none';
}
function exportSQL() {
  var qs = collectQuery();
  var cfq = collectColFilterQuery();
  if (cfq) qs += cfq;
  window.open('/online/cgreport/api/exportSQLInsert/' + encodeURIComponent(code) + '?' + qs + (shareToken ? '&share=' + encodeURIComponent(shareToken) : ''));
  $('#exportMenu').style.display = 'none';
}

/* ───── 导入 Excel（首行表头=字段名，批量写回主表）───── */
function doImportExcel(input) {
  var file = input.files && input.files[0];
  input.value = '';
  if (!file) return;
  if (!confirm('确认把 ' + file.name + ' 的数据导入报表主表？首行需为字段名。')) return;
  var fd = new FormData();
  fd.append('file', file);
  fetch('/online/cgreport/api/importExcel/' + encodeURIComponent(code), { method: 'POST', body: fd })
    .then(function (res) { return res.json(); })
    .then(function (r) {
      if (r.code === 401) { location.href = '/login.html'; return; }
      if (r.success) { toast('已导入 ' + r.result.imported + ' 行', 'success'); loadData(); }
      else { toast(r.message || '导入失败', 'error'); }
    })
    .catch(function (e) { toast(e.message, 'error'); });
}

/* ───── 定时自动刷新 ───── */
var refreshTimer = null;
function setAutoRefresh(sec) {
  if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null; }
  sec = parseInt(sec) || 0;
  if (sec > 0) refreshTimer = setInterval(loadData, sec * 1000);
}

/* ───── 导出 CSV（needCount=false 跳过服务端 COUNT，含合计行）───── */
function exportCsv() {
  var m = $('#exportMenu'); if (m) m.style.display = 'none';
  var qs = 'pageNo=1&pageSize=100000&needCount=false&' + (hasTotal ? 'needSummary=true&' : '') + collectQuery();
  var cfq = collectColFilterQuery();
  if (cfq) qs += cfq;
  api('/online/cgreport/api/getData/' + encodeURIComponent(code) + '?' + qs)
    .then(function (r) {
      var rows = r.records || [];
      var head = showFields.map(function (f) { return f.fieldTxt || f.fieldName; });
      var lines = [head.join(',')].concat(rows.map(function (rec) {
        return showFields.map(function (f) {
          var v = fmtCell(rec[f.fieldName]);
          return /[",\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v;
        }).join(',');
      }));
      if (r.summary && Object.keys(r.summary).length) {
        lines.push(showFields.map(function (f) {
          var v = r.summary[f.fieldName];
          return v !== undefined && v !== null ? fmtNum(v) : '';
        }).join(','));
      }
      var blob = new Blob(['\ufeff' + lines.join('\n')], { type: 'text/csv;charset=utf-8' });
      var a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = (code || 'report') + '_' + Date.now() + '.csv';
      a.click(); URL.revokeObjectURL(a.href);
      toast('导出成功，共 ' + rows.length + ' 条', 'success');
    })
    .catch(function (e) { toast(e.message, 'error'); });
}

/* ───── 交叉表 / 分组汇总（复用公共聚合接口）───── */
function openCross() { openPivot('cross'); }
function openGroupSum() { openPivot('group'); }
function openPivot(mode) {
  $('#pvtTitle').textContent = mode === 'cross' ? '交叉表（行×列 聚合）' : '分组汇总（按维度聚合合计列）';
  $('#pvtModal').dataset.mode = mode;
  var opts = showFields.map(function (f) { return '<option>' + esc(f.fieldName) + '</option>'; }).join('');
  $('#pvRow').innerHTML = opts;
  $('#pvCol').innerHTML = opts;
  $('#pvMeasure').innerHTML = '<option value="">计数</option>' + opts;
  $('#pvColWrap').style.display = mode === 'cross' ? 'flex' : 'none';
  $('#pvtBody').innerHTML = '<div class="empty-tip">选择字段后点击「分析」</div>';
  openModal('pvtModal');
}
function runPivot() {
  var mode = $('#pvtModal').dataset.mode;
  var qs = collectQuery();
  if (qs) qs += '&';
  var box = $('#pvtBody');
  box.innerHTML = '<div class="empty-tip">分析中...</div>';
  if (mode === 'cross') {
    var url = '/online/cgreport/api/getCrossTable/' + encodeURIComponent(code) + '?' + qs +
      'row=' + encodeURIComponent($('#pvRow').value) + '&col=' + encodeURIComponent($('#pvCol').value) +
      '&measure=' + encodeURIComponent($('#pvMeasure').value) + '&agg=' + $('#pvAgg').value;
    api(url).then(function (ct) {
      var html = '<table class="grid"><thead><tr><th>' + esc($('#pvRow').value) + '</th>';
      (ct.columns || []).forEach(function (c) { html += '<th>' + esc(c) + '</th>'; });
      html += '</tr></thead><tbody>';
      (ct.rows || []).forEach(function (row) {
        html += '<tr><td><b>' + esc(row.__row) + '</b></td>';
        (ct.columns || []).forEach(function (c) { html += '<td>' + esc(fmtCell(row[c])) + '</td>'; });
        html += '</tr>';
      });
      html += '</tbody></table>';
      box.innerHTML = html;
    }).catch(function (e) { box.innerHTML = '<div class="empty-tip" style="color:#ff4d4f">' + esc(e.message) + '</div>'; });
  } else {
    // 分组汇总：维度=所选字段，度量优先取合计列配置，否则计数
    var totalFields = fields.filter(function (f) { return f.isTotal === 'Y' && f.fieldType === '数值类型'; });
    var dim = $('#pvRow').value;
    var head = '<tr><th>' + esc(dim) + '</th>';
    var reqs = [];
    if (totalFields.length) {
      totalFields.forEach(function (f) {
        head += '<th>SUM(' + esc(f.fieldName) + ')</th>';
        reqs.push({ m: f.fieldName, a: 'sum' });
      });
    } else {
      head += '<th>计数</th>';
      reqs.push({ m: '', a: 'count' });
    }
    head += '</tr>';
    box.innerHTML = '<table class="grid"><thead>' + head + '</thead><tbody id="gsBody"><tr><td colspan="' + (reqs.length + 1) + '" class="empty-tip">加载中...</td></tr></tbody></table>';
    reqs.forEach(function (rq, i) {
      var url = '/online/cgreport/api/getChartData/' + encodeURIComponent(code) + '?' + qs +
        'dim=' + encodeURIComponent(dim) + '&measure=' + encodeURIComponent(rq.m) + '&agg=' + rq.a + '&limit=50';
      api(url).then(function (r) {
        var pts = r.points || [];
        var rows = pts.map(function (p) { return '<tr><td>' + esc(p.name) + '</td><td>' + esc(fmtCell(p.value)) + '</td></tr>'; }).join('');
        var tb = $('#gsBody');
        if (i === 0) tb.innerHTML = rows || '<tr><td colspan="2" class="empty-tip">无数据</td></tr>';
        else {
          // 追加列
          var trs = tb.querySelectorAll('tr');
          pts.forEach(function (p, ri) {
            if (trs[ri]) trs[ri].insertAdjacentHTML('beforeend', '<td>' + esc(fmtCell(p.value)) + '</td>');
          });
        }
      }).catch(function (e) { toast(e.message, 'error'); });
    });
  }
}
