/* index-app.js —— 由页面内联脚本外置（LiteReport） */
renderNav('Online报表配置');

var state = { list: [], total: 0, page: 1, dsList: [], itemChecked: {}, paramChecked: {}, edit: { items: [], params: [] } };

function pageSize() { return parseInt(($('#pageSize').value || '10')); }

function reload(page) { listTable.reload(page); }

var listTable = new LiteTable({
  table: '#listTable',
  pager: '#pages',
  totalEl: '#total',
  ellipsis: true,
  sizeOf: function () { return pageSize(); },
  columns: [
    { title: '<input type="checkbox" onchange="toggleAll(this)">', td: function (h, idx) {
        return '<input type="checkbox" class="row-check" data-idx="' + idx + '" onchange="selChanged()">';
      } },
    { title: '报表名字', field: 'reportName' },
    { title: '报表编码', td: function (h) { return '<span class="mono">' + esc(h.reportCode) + '</span>'; } },
    { title: '报表SQL', td: function (h) {
        return '<span class="left sql-cell" title="' + esc(h.cgSql) + '">' + esc(h.cgSql) + '</span>';
      } },
    { title: '数据源', td: function (h) {
        var ds = state.dsList.find(function (d) { return d.key === h.dbSource; });
        return esc(ds ? ds.name : (h.dbSource || ''));
      } },
    { title: '分类', td: function (h) { return esc(h.category || '-'); } },
    { title: '创建时间', field: 'createTime' },
    { title: '操作', td: function (h) {
        return '<a href="javascript:void(0)" onclick="openEdit(\'' + h.id + '\')">编辑</a> ' +
          '<span class="more-wrap"><a href="javascript:void(0)" onclick="toggleMore(this)">更多 ∨</a>' +
          '<span class="more-menu">' +
          '<a onclick="funTest(\'' + h.id + '\')">功能测试</a>' +
          '<a onclick="showUrl(\'' + h.id + '\')">配置地址</a>' +
          '<a onclick="showVersions(\'' + h.id + '\')">版本历史</a>' +
          '<a onclick="shareLink(\'' + h.id + '\')">分享链接</a>' +
          '<a onclick="copyConfig(\'' + h.id + '\')">复制配置</a>' +
          '<a onclick="exportCfg(\'' + h.id + '\')">导出配置</a>' +
          '<a class="danger" onclick="delReport(\'' + h.id + '\')">删除</a>' +
          '</span></span>';
      } },
  ],
  emptyText: '暂无数据',
  onLoaded: function (rows) { state.list = rows || []; selChanged(); },
  fetch: function (page, size) {
    return api('/online/cgreport/head/list?pageNo=' + page + '&pageSize=' + size +
        '&reportName=' + encodeURIComponent($('#qName').value) +
        '&reportCode=' + encodeURIComponent($('#qCode').value) +
        '&category=' + encodeURIComponent($('#qCat').value)).then(function (r) {
      return { rows: r.records || [], total: r.total };
    });
  }
});
function toggleMore(a) {
  var menu = a.nextElementSibling;
  var wasOpen = menu.classList.contains('show');
  $$('.more-menu.show').forEach(function (m) { if (m !== menu) m.classList.remove('show'); });
  if (!wasOpen) {
    // fixed 定位：按触发链接的视口坐标展开（表格单元格 overflow:hidden 会裁剪绝对定位菜单）
    var r = a.getBoundingClientRect();
    menu.style.top = (r.bottom + 4) + 'px';
    menu.style.right = (window.innerWidth - r.right) + 'px';
    menu.classList.add('show');
  }
}
document.addEventListener('click', function (e) {
  if (!e.target.closest('.more-wrap')) $$('.more-menu.show').forEach(function (m) { m.classList.remove('show'); });
});
function toggleAll(cb) { $$('.row-check').forEach(function (c) { c.checked = cb.checked; }); selChanged(); }
function selChanged() {
  var n = $$('.row-check:checked').length;
  $('#selTip').textContent = n > 0 ? '已选中 ' + n + ' 条数据' : '未选中任何数据';
}

function loadDs() {
  return api('/api/datasource/list').then(function (list) {
    state.dsList = list || [];
    $('#fDs').innerHTML = state.dsList.map(function (d) {
      return '<option value="' + esc(d.key) + '">' + esc(d.name) + ' (' + d.type + (d.status === 'online' ? '' : '·离线') + ')</option>';
    }).join('');
  });
}

/* ───── 编辑弹窗 ───── */
var TYPE_OPTS = ['字符类型', '数值类型', '日期类型'];
var MODE_OPTS = ['=', '!=', '>', '>=', '<', '<=', 'like', 'not like', 'in', 'between'];

function openCreate() {
  $('#editTitle').textContent = '新增';
  $('#fCode').value = ''; $('#fName').value = ''; $('#fSql').value = ''; $('#fCat').value = '';
  $('#fPerm').value = ''; $('#fPermOn').checked = false;
  $('#previewBox').style.display = 'none';
  if (state.dsList.length) $('#fDs').value = state.dsList[0].key;
  state.itemChecked = {}; state.paramChecked = {};
  state.edit = { id: '', items: [], params: [] };
  renderItems(); renderParams();
  openModal('editModal');
}
function openEdit(id) {
  api('/online/cgreport/head/' + id).then(function (r) {
    var h = r.head;
    $('#editTitle').textContent = '编辑';
    $('#fCode').value = h.reportCode; $('#fName').value = h.reportName;
    $('#fSql').value = h.cgSql; $('#fDs').value = h.dbSource || 'local';
    $('#fCat').value = h.category || '';
    $('#fPerm').value = h.permFilter || ''; $('#fPermOn').checked = h.permEnabled === 'Y';
    $('#previewBox').style.display = 'none';
    state.itemChecked = {}; state.paramChecked = {};
    state.edit = { id: h.id, items: r.items || [], params: r.params || [] };
    renderItems(); renderParams();
    openModal('editModal');
  }).catch(function (e) { toast(e.message, 'error'); });
}
// 试运行：执行当前SQL前10行（不落配置）
function previewSql(confirmed) {
  var sql = $('#fSql').value.trim();
  if (!sql) { toast('请先输入报表SQL', 'error'); return; }
  var box = $('#previewBox');
  box.style.display = 'block';
  box.innerHTML = '<div class="empty-tip">执行中...</div>';
  var doReq = function (confirmed) {
    api('/online/cgreport/sql/preview', { method: 'POST', body: { sql: sql, dbSource: $('#fDs').value, confirmed: confirmed } })
      .then(function (r) {
        if (r.needConfirm) {
          var html = '<div class="empty-tip" style="color:#ff4d4f">⚠ 检测到危险 SQL：</div><ul style="margin:6px 0">';
          (r.reasons || []).forEach(function (x) { html += '<li>' + esc(x) + '</li>'; });
          html += '</ul><button class="btn btn-danger" onclick="previewSql(true)">我已了解风险，仍要执行</button>';
          box.innerHTML = html;
          return;
        }
        var cols = r.columns || [], recs = r.records || [];
        var html = '<table class="edit" style="min-width:400px"><thead><tr>';
        cols.forEach(function (c) { html += '<th>' + esc(c) + '</th>'; });
        html += '</tr></thead><tbody>';
        recs.forEach(function (rec) {
          html += '<tr>';
          cols.forEach(function (c) { html += '<td>' + esc(fmtCell(rec[c])) + '</td>'; });
          html += '</tr>';
        });
        html += '</tbody></table>';
        box.innerHTML = html;
        if (!recs.length) box.innerHTML += '<div class="muted" style="padding:6px">查询成功，无数据</div>';
      })
      .catch(function (e) { box.innerHTML = '<div class="empty-tip" style="color:#ff4d4f">' + esc(e.message) + '</div>'; });
  };
  doReq(confirmed === true);
}
function switchTab(el) {
  $$('.tabs .tab').forEach(function (t) { t.classList.toggle('active', t === el); });
  $$('.tab-pane').forEach(function (p) { p.classList.toggle('active', p.id === el.dataset.tab); });
}
function addItemRow() {
  state.edit.items.push({ fieldName: '', fieldTxt: '', fieldType: '字符类型', isShow: 'Y', isQuery: 'N', queryMode: '=', isTotal: 'N' });
  renderItems();
}
function addParamRow() {
  state.edit.params.push({ paramName: '', paramTxt: '', paramValue: '' });
  renderParams();
}
var ITEM_INPUTS = [
  ['fieldName', 'text', ''], ['fieldTxt', 'text', ''], ['width', 'text', 'w40'],
  ['fieldType', 'select:字符类型,数值类型,日期类型', ''],
  ['__isShow', 'check:列显示', ''], ['fieldHref', 'text', ''],
  ['__isQuery', 'check:查询', ''],
  ['queryMode', 'select:=,!=,>,>=,<,<=,like,not like,in,between', ''],
  ['expr', 'text', ''], ['dictCode', 'text', ''], ['groupTitle', 'text', ''],
  ['__isTotal', 'check:合计', ''],
  ['renderRule', 'text', '']
];
function renderItems() {
  var tbody = $('#itemsBody');
  var checked = {};
  Object.keys(state.itemChecked).forEach(function (i) { if (+i < state.edit.items.length) checked[i] = true; });
  state.itemChecked = checked;
  if (!state.edit.items.length) {
    tbody.innerHTML = '<tr><td colspan="17" class="empty-tip">暂无数据，可点击“新增”或使用“SQL解析”自动生成</td></tr>';
    return;
  }
  tbody.innerHTML = state.edit.items.map(function (it, i) {
    var tds = '<td class="muted">≡</td><td><input type="checkbox" class="item-check" data-i="' + i + '" ' + (state.itemChecked[i] ? 'checked' : '') + '></td><td>' + (i + 1) + '</td>';
    ITEM_INPUTS.forEach(function (def) {
      var key = def[0], cls = def[2];
      if (def[1] === 'check:列显示') {
        tds += '<td><input type="checkbox" data-i="' + i + '" data-k="isShow" ' + (it.isShow !== 'N' ? 'checked' : '') + '></td>';
      } else if (def[1] === 'check:查询') {
        tds += '<td><input type="checkbox" data-i="' + i + '" data-k="isQuery" ' + (it.isQuery === 'Y' ? 'checked' : '') + '></td>';
      } else if (def[1] === 'check:合计') {
        tds += '<td><input type="checkbox" data-i="' + i + '" data-k="isTotal" ' + (it.isTotal === 'Y' ? 'checked' : '') + '></td>';
      } else if (def[1].indexOf('select:') === 0) {
        var opts = def[1].slice(7).split(',');
        tds += '<td><select data-i="' + i + '" data-k="' + key + '">' +
          opts.map(function (o) { return '<option ' + (it[key] === o ? 'selected' : '') + '>' + o + '</option>'; }).join('') + '</select></td>';
      } else {
        tds += '<td><input type="text" class="' + cls + '" data-i="' + i + '" data-k="' + key + '" value="' + esc(it[key] || '') + '"></td>';
      }
    });
    return tds + '<td><a class="btn-link danger" onclick="delItemRow(' + i + ')">删除</a></td></tr>';
  }).join('');
}
function renderParams() {
  var tbody = $('#paramsBody');
  var checked = {};
  Object.keys(state.paramChecked).forEach(function (i) { if (+i < state.edit.params.length) checked[i] = true; });
  state.paramChecked = checked;
  if (!state.edit.params.length) {
    tbody.innerHTML = '<tr><td colspan="6" class="empty-tip">暂无数据</td></tr>';
    return;
  }
  tbody.innerHTML = state.edit.params.map(function (p, i) {
    return '<td><input type="checkbox" class="param-check" data-i="' + i + '" ' + (state.paramChecked[i] ? 'checked' : '') + '></td><td>' + (i + 1) + '</td>' +
      '<td><input type="text" data-p="' + i + '" data-k="paramName" value="' + esc(p.paramName) + '"></td>' +
      '<td><input type="text" data-p="' + i + '" data-k="paramTxt" value="' + esc(p.paramTxt) + '"></td>' +
      '<td><input type="text" data-p="' + i + '" data-k="paramValue" value="' + esc(p.paramValue) + '"></td>' +
      '<td><a class="btn-link danger" onclick="delParamRow(' + i + ')">删除</a></td></tr>';
  }).join('');
}
$('#itemsBody') && $('#itemsBody').addEventListener('input', function (e) {
  var el = e.target, i = el.dataset.i, k = el.dataset.k;
  if (i === undefined || !k) return;
  if (el.type === 'checkbox') state.edit.items[+i][k] = el.checked ? 'Y' : 'N';
  else state.edit.items[+i][k] = el.value;
});
$('#itemsBody') && $('#itemsBody').addEventListener('change', function (e) {
  var el = e.target, i = el.dataset.i, k = el.dataset.k;
  if (i === undefined || !k) return;
  if (el.type === 'checkbox') state.edit.items[+i][k] = el.checked ? 'Y' : 'N';
  else state.edit.items[+i][k] = el.value;
});
$('#paramsBody') && $('#paramsBody').addEventListener('input', function (e) {
  var el = e.target, i = el.dataset.p, k = el.dataset.k;
  if (i === undefined || !k) return;
  state.edit.params[+i][k] = el.value;
});
function delItemRow(i) { state.edit.items.splice(i, 1); renderItems(); }
function delParamRow(i) { state.edit.params.splice(i, 1); renderParams(); }

function parseSql() {
  var sql = $('#fSql').value.trim();
  if (!sql) { toast('请先输入报表SQL', 'error'); return; }
  api('/online/cgreport/sql/parse', { method: 'POST', body: { sql: sql, dbSource: $('#fDs').value } })
    .then(function (r) {
      var fields = r.fields || [];
      var old = {};
      state.edit.items.forEach(function (it) { if (it.fieldName) old[it.fieldName.toLowerCase()] = it; });
      state.edit.items = fields.map(function (f) {
        var prev = old[f.name.toLowerCase()];
        return prev ? Object.assign({}, prev, { fieldType: f.type }) : {
          fieldName: f.name, fieldTxt: f.text, fieldType: f.type,
          isShow: 'Y', isQuery: 'N', queryMode: '=', isTotal: 'N'
        };
      });
      renderItems();
      toast('解析成功，共 ' + fields.length + ' 个字段', 'success');
    })
    .catch(function (e) { toast(e.message, 'error'); });
}

function saveReport() {
  var code = $('#fCode').value.trim(), name = $('#fName').value.trim(), sql = $('#fSql').value.trim();
  if (!code || !name || !sql) { toast('报表编码、报表名字、报表SQL为必填项', 'error'); return; }
  var items = state.edit.items.filter(function (it) { return it.fieldName && it.fieldTxt; });
  var params = state.edit.params.filter(function (p) { return p.paramName && p.paramTxt; });
  if (items.length && items.some(function (it) { return !it.fieldName || !it.fieldTxt; })) {
    toast('字段明细中存在未填写的字段名字/字段文本', 'error'); return;
  }
  var body = {
    id: state.edit.id, reportCode: code, reportName: name, cgSql: sql,
    dbSource: $('#fDs').value, category: $('#fCat').value.trim(), items: items, params: params,
    permFilter: $('#fPerm').value.trim(), permEnabled: $('#fPermOn').checked ? 'Y' : 'N'
  };
  api('/online/cgreport/head', { method: 'POST', body: body })
    .then(function () { toast('保存成功', 'success'); closeModal('editModal'); reload(1); })
    .catch(function (e) { toast(e.message, 'error'); });
}

function funTest(id) {
  // 功能测试：跳转 AUTO在线报表
  window.open('/online/cgreport/' + id);
}
var urlCache = { id: '', sql: '' };
function showUrl(id) {
  var h = state.list.find(function (x) { return x.id === id; });
  if (!h) return;
  urlCache = { id: id, sql: h.cgSql };
  $('#urlTitle').textContent = '菜单链接【' + h.reportName + '】';
  $('#urlText').textContent = location.origin + '/online/cgreport/' + id;
  openModal('urlModal');
}
function copyUrl() { copyText($('#urlText').textContent); }
function copySql() { copyText(urlCache.sql); }
function delReport(id) {
  if (!confirm('确认删除该报表配置吗？')) return;
  api('/online/cgreport/head/' + id, { method: 'DELETE' })
    .then(function () { toast('删除成功', 'success'); reload(); })
    .catch(function (e) { toast(e.message, 'error'); });
}
function aiGenerate() {
  var table = $('#aiTable').value.trim().replace(/[^A-Za-z0-9_.]/g, '');
  if (!table) { toast('请输入数据表名', 'error'); return; }
  closeModal('aiModal');
  openCreate();
  $('#editTitle').textContent = 'AI生成报表';
  $('#fCode').value = table.toLowerCase() + '_rep';
  $('#fName').value = table + ' 数据报表';
  $('#fSql').value = 'select * from ' + table;
  toast('已生成报表草稿，点击「SQL解析」获取字段', 'info');
}

function showSqlHistory(page) {
  page = page || 1;
  api('/api/sqlhistory?pageNo=' + page + '&pageSize=20').then(function (r) {
    var list = r.records || [];
    var html = list.map(function (h) {
      return '<tr><td>' + esc(h.createTime) + '</td><td>' + esc(h.userName) + '</td>' +
        '<td class="mono">' + esc(h.dbSource) + '</td>' +
        '<td>' + h.durationMs + 'ms</td><td>' + h.rowCount + '</td>' +
        '<td>' + (h.ok === 'Y' ? '<span style="color:#52c41a">OK</span>' : '<span style="color:#ff4d4f">失败</span>') + '</td>' +
        '<td class="left mono" style="max-width:400px;overflow:hidden;text-overflow:ellipsis" title="' + esc(h.sqlText) + '">' + esc(h.sqlText) + '</td></tr>';
    }).join('') || '<tr><td colspan="7" class="empty-tip">暂无记录</td></tr>';
    $('#histBody').innerHTML = html;
    $('#histTotal').textContent = r.total;
    var pages = r.pages || 1, cur = r.current || 1;
    var h = '<span class="pg' + (cur <= 1 ? ' disabled' : '') + '" onclick="showSqlHistory(' + (cur - 1) + ')">&lt;</span>';
    for (var i = 1; i <= pages; i++) h += '<span class="pg' + (i === cur ? ' cur' : '') + '" onclick="showSqlHistory(' + i + ')">' + i + '</span>';
    h += '<span class="pg' + (cur >= pages ? ' disabled' : '') + '" onclick="showSqlHistory(' + (cur + 1) + ')">&gt;</span>';
    $('#histPages').innerHTML = h;
    openModal('histModal');
  }).catch(function (e) { toast(e.message, 'error'); });
}

function showVersions(id) {
  api('/online/cgreport/head-versions?headId=' + id).then(function (list) {
    var t = $('#verTitle');
    if (t) t.textContent = '版本历史（共' + (list || []).length + '版）';
    $('#verBody').innerHTML = (list || []).map(function (v) {
      return '<tr><td>' + esc(v.createTime) + '</td><td>' + esc(v.createBy) + '</td>' +
        '<td><a onclick="rollback(\'' + v.id + '\')">回滚此版本</a></td></tr>';
    }).join('') || '<tr><td colspan="3" class="empty-tip">暂无版本</td></tr>';
    openModal('verModal');
  }).catch(function (e) { toast(e.message, 'error'); });
}
function rollback(vid) {
  if (!confirm('确认回滚到该版本吗？')) return;
  api('/online/cgreport/head-version/' + vid + '/rollback', { method: 'POST' }).then(function () {
    toast('已回滚', 'success'); closeModal('verModal'); reload(1);
  }).catch(function (e) { toast(e.message, 'error'); });
}
function exportCfg(id) { window.open('/online/cgreport/head-export/' + id); }
// 分享链接：生成免登录只读访问地址（有效期可选，0=永久）
function shareLink(id) {
  var days = prompt("分享有效期（天，留空或 0 = 永久有效）:", "7");
  if (days === null) return;
  api("/online/cgreport/head-share/" + id, { method: "POST", body: { days: parseInt(days) || 0 } })
    .then(function (r) {
      var h = state.list.find(function (x) { return x.id === id; });
      $("#urlTitle").textContent = "分享链接【" + (h ? h.reportName : "") + "】(至 " + r.expire + ")";
      $("#urlText").textContent = r.url;
      urlCache = { id: id, sql: r.url }; // 复制按钮复用
      openModal("urlModal");
      toast("分享链接已生成", "success");
    })
    .catch(function (e) { toast(e.message, "error"); });
}
function openImport() { $('#impFile').value = ''; $('#impJson').value = ''; openModal('impModal'); }
function doImport() {
  var f = $('#impFile').files[0];
  if (f) { var rd = new FileReader(); rd.onload = function () { $('#impJson').value = rd.result; sendImport(); }; rd.readAsText(f); return; }
  sendImport();
}
function sendImport() {
  var txt = $('#impJson').value.trim();
  if (!txt) { toast('请选择文件或粘贴JSON', 'error'); return; }
  api('/online/cgreport/head/import', { method: 'POST', body: txt }).then(function (h) {
    toast('已导入: ' + h.reportCode, 'success'); closeModal('impModal'); reload(1);
  }).catch(function (e) { toast(e.message, 'error'); });
}
function aiGenSQL() {
  var table = $('#aiTable').value.trim(), desc = $('#aiDesc').value.trim();
  if (!table || !desc) { toast('AI生成需填写表名和需求描述（留空描述用快速生成）', 'error'); return; }
  var btn = $('#aiGenBtn'); btn.disabled = true; btn.textContent = '生成中...';
  api('/api/ai/sql', { method: 'POST', body: { desc: desc, table: table, dbSource: ($('#fDs') && $('#fDs').value) || 'local' } })
    .then(function (r) {
      closeModal('aiModal'); openCreate();
      $('#editTitle').textContent = 'AI生成报表';
      $('#fCode').value = table.toLowerCase() + '_ai';
      $('#fName').value = desc;
      $('#fSql').value = r.sql;
      toast('AI已生成SQL，请点击SQL解析', 'success');
    })
    .catch(function (e) { toast(e.message, 'error'); })
    .finally(function () { btn.disabled = false; btn.textContent = '🤖 AI生成SQL'; });
}
// 顶栏 AI助手 跳转入口：自动打开 AI生成报表弹窗
if (location.search.indexOf('ai=1') >= 0) openModal('aiModal');
loadDs().then(function () { reload(1); });

// ───── 明细/参数行勾选与批量删除 ─────
$('#itemsBody').addEventListener('change', function (e) {
  var el = e.target;
  if (el.classList && el.classList.contains('item-check')) {
    if (el.checked) state.itemChecked[el.dataset.i] = true;
    else delete state.itemChecked[el.dataset.i];
  }
});
$('#paramsBody').addEventListener('change', function (e) {
  var el = e.target;
  if (el.classList && el.classList.contains('param-check')) {
    if (el.checked) state.paramChecked[el.dataset.i] = true;
    else delete state.paramChecked[el.dataset.i];
  }
});
function toggleAllItems(cb) {
  state.itemChecked = {};
  $$('.item-check').forEach(function (c) { c.checked = cb.checked; if (cb.checked) state.itemChecked[c.dataset.i] = true; });
}
function toggleAllParams(cb) {
  state.paramChecked = {};
  $$('.param-check').forEach(function (c) { c.checked = cb.checked; if (cb.checked) state.paramChecked[c.dataset.i] = true; });
}
function delCheckedItems() {
  var n = Object.keys(state.itemChecked).length;
  if (!n) { toast('请先勾选要删除的行', 'error'); return; }
  if (!confirm('确认删除选中的 ' + n + ' 行字段明细？')) return;
  state.edit.items = state.edit.items.filter(function (it, i) { return !state.itemChecked[i]; });
  state.itemChecked = {};
  renderItems();
}
function delCheckedParams() {
  var n = Object.keys(state.paramChecked).length;
  if (!n) { toast('请先勾选要删除的行', 'error'); return; }
  if (!confirm('确认删除选中的 ' + n + ' 行报表参数？')) return;
  state.edit.params = state.edit.params.filter(function (p, i) { return !state.paramChecked[i]; });
  state.paramChecked = {};
  renderParams();
}

// 复制配置：以副本方式新建（编码加时间戳后缀防冲突），成功后直接打开编辑
function copyConfig(id) {
  api('/online/cgreport/head/' + id).then(function (r) {
    var h = r.head;
    var suffix = '_' + Date.now() % 100000;
    var body = {
      reportCode: h.reportCode + suffix,
      reportName: h.reportName + ' (副本)',
      cgSql: h.cgSql, dbSource: h.dbSource, category: h.category,
      permFilter: h.permFilter, permEnabled: h.permEnabled,
      items: (r.items || []).map(function (it) { var c = Object.assign({}, it); delete c.id; return c; }),
      params: (r.params || []).map(function (pa) { var c = Object.assign({}, pa); delete c.id; return c; })
    };
    return api('/online/cgreport/head', { method: 'POST', body: body }).then(function (nh) {
      toast('已复制为 ' + nh.reportCode + '，可编辑后保存', 'success');
      reload(1);
      openEdit(nh.id);
    });
  }).catch(function (e) { toast(e.message, 'error'); });
}
