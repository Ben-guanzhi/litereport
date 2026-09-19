/* LiteReport 平台公共 JS 工具 */
(function () {
  window.$ = function (sel, root) { return (root || document).querySelector(sel); };
  window.$$ = function (sel, root) { return Array.from((root || document).querySelectorAll(sel)); };

  window.esc = function (s) {
    if (s === null || s === undefined) return '';
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  };

  window.toast = function (msg, type) {
    var wrap = $('#toast-wrap');
    if (!wrap) {
      wrap = document.createElement('div');
      wrap.id = 'toast-wrap';
      document.body.appendChild(wrap);
    }
    var t = document.createElement('div');
    t.className = 'toast ' + (type || 'info');
    t.textContent = msg;
    wrap.appendChild(t);
    setTimeout(function () { t.style.opacity = '0'; t.style.transition = 'opacity .3s'; }, 2400);
    setTimeout(function () { t.remove(); }, 2800);
  };

  // 统一 API 请求：返回 result 或抛错；401 自动跳转登录页
  window.api = function (url, options) {
    options = options || {};
    var init = {
      method: options.method || 'GET',
      headers: { 'Content-Type': 'application/json;charset=utf-8' }
    };
    if (options.body !== undefined) init.body = typeof options.body === 'string' ? options.body : JSON.stringify(options.body);
    return fetch(url, init).then(function (res) {
      // 网关/代理返回非 JSON（如 502 HTML）时给出可读错误
      var ct = res.headers.get('content-type') || '';
      if (ct.indexOf('json') < 0) {
        throw new Error('服务异常(HTTP ' + res.status + ')，请稍后重试或联系管理员');
      }
      return res.json().catch(function () {
        throw new Error('服务响应解析失败(HTTP ' + res.status + ')');
      });
    }).then(function (r) {
      if (r.code === 401) {
        var next = encodeURIComponent(location.pathname + location.search);
        location.href = '/login.html?next=' + next;
        throw new Error('未登录');
      }
      if (r.success) return r.result;
      throw new Error(r.message || '请求失败');
    });
  };

  window.copyText = function (text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text).then(function () { toast('复制成功', 'success'); },
        function () { fallbackCopy(text); });
    }
    fallbackCopy(text);
    return Promise.resolve();
  };
  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text; document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); toast('复制成功', 'success'); } catch (e) { toast('复制失败', 'error'); }
    ta.remove();
  }

  window.openModal = function (id) { $('#' + id).classList.add('show'); };
  window.closeModal = function (id) { $('#' + id).classList.remove('show'); };

  // 顶栏（activeTab: 当前高亮的标签文字）。自动注入顶部进度条，页面无需重复书写。
  window.renderNav = function (active) {
    if (!$('.topbar')) {
      var tb = document.createElement('div');
      tb.className = 'topbar';
      tb.innerHTML = '<div class="bar"></div>';
      document.body.insertBefore(tb, document.body.firstChild);
    }
    var tabs = [
      { text: '个人工作台', href: '/workspace.html' },
      { text: '工作台', href: '/workspace.html' },
      { text: 'Online表单开发', href: '/form.html' },
      { text: 'Online图表配置', href: '/chart.html' },
      { text: 'Online报表配置', href: '/index.html' },
      { text: '字典管理', href: '/dict.html' }
    ];
    var el = $('#nav');
    if (!el) return;
    var html = '';
    tabs.forEach(function (t) {
      html += '<a class="tab' + (t.text === active ? ' active' : '') + '" href="' + t.href + '">' + t.text + '</a>';
    });
    html +=
      '<span class="spacer"></span>' +
      '<a class="tab" href="/api.html" title="公共接口文档">🔗公共接口</a>' +
      '<a class="tab only-admin" href="/users.html" title="用户与角色">👥用户</a>' +
      '<a class="tab" href="/integrate.html" title="第三方集成指南">🔌集成指南</a>' +
      '<a class="tab only-admin" href="/datasources.html" title="数据源管理">🗄数据源</a>' +
      '<a class="tab only-admin" href="/audit.html" title="审计日志">📋审计</a>' +
      '<a class="tab only-admin" href="/push.html" title="定时推送">📤推送</a>' +
      '<a class="tab" id="navPwd" style="display:none" onclick="openPwdModal()">🔑改密</a>' +
      '<span class="tab" id="navUser" style="display:none"></span>' +
      '<a class="tab" id="navLogout" style="display:none" onclick="logout()">退出</a>' +
      '<span class="ai" title="AI助手">🤖AI助手</span>';
    el.innerHTML = html;
    // AI助手入口：跳转 AI 对话页面
    var aiBtn = el.querySelector('.ai');
    if (aiBtn) {
      aiBtn.style.cursor = 'pointer';
      aiBtn.onclick = function () { location.href = '/ai.html'; };
    }
    // 会话状态：显示用户名、退出与改密入口
    fetch('/api/auth/me').then(function (r) { return r.json(); }).then(function (r) {
      if (r.success && r.result && r.result.username) {
        window._currentUser = r.result.username;
        window._currentRole = (r.result.role || 'admin').toLowerCase();
        $('#navUser').textContent = '👤' + r.result.username + '(' + (r.result.role || 'admin') + ')';
        $('#navUser').style.display = '';
        $('#navLogout').style.display = '';
        $('#navPwd').style.display = '';
        if (window.applyRoleVisibility) window.applyRoleVisibility();
      }
    }).catch(function () {});
  };

  // 角色隐藏工具：data-role="admin|editor" 的元素，非匹配角色 display:none
  window.applyRoleVisibility = function () {
    var role = (window._currentRole || 'admin').toLowerCase();
    var rank = {viewer: 1, editor: 2, admin: 3};
    document.querySelectorAll('[data-role]').forEach(function (el) {
      var need = el.getAttribute('data-role');
      if (rank[role] >= rank[need]) el.style.display = '';
      else el.style.display = 'none';
    });
    // 通用 is-admin 简化类
    var adminOnly = document.querySelectorAll('.only-admin');
    if (rank[role] < 3) adminOnly.forEach(function (el) { el.style.display = 'none'; });
    else adminOnly.forEach(function (el) { el.style.display = ''; });
  };

  // ───── 修改密码（全局弹窗，按需注入）─────
  window.openPwdModal = function () {
    var m = $('#pwdModal');
    if (!m) {
      m = document.createElement('div');
      m.className = 'mask';
      m.id = 'pwdModal';
      m.innerHTML = '<div class="modal narrow"><div class="modal-head"><span class="title">修改密码</span>' +
        '<button class="close" onclick="closeModal(\x27pwdModal\x27)">✕</button></div>' +
        '<div class="modal-body">' +
        '<label>旧密码:</label><input type="password" class="input" style="width:100%" id="pwdOld">' +
        '<label style="display:block;margin-top:10px">新密码(≥6位):</label><input type="password" class="input" style="width:100%" id="pwdNew">' +
        '<label style="display:block;margin-top:10px">确认新密码:</label><input type="password" class="input" style="width:100%" id="pwdNew2">' +
        '<p class="muted mt8">修改成功后需重新登录。</p>' +
        '</div><div class="modal-foot">' +
        '<button class="btn" onclick="closeModal(\x27pwdModal\x27)">取消</button>' +
        '<button class="btn btn-primary" onclick="doChangePwd()">确认修改</button>' +
        '</div></div>';
      document.body.appendChild(m);
    }
    $('#pwdOld').value = ''; $('#pwdNew').value = ''; $('#pwdNew2').value = '';
    openModal('pwdModal');
  };
  window.doChangePwd = function () {
    var o = $('#pwdOld').value, n = $('#pwdNew').value, n2 = $('#pwdNew2').value;
    if (!o || !n) { toast('请填写完整', 'error'); return; }
    if (n.length < 6) { toast('新密码长度至少6位', 'error'); return; }
    if (n !== n2) { toast('两次新密码不一致', 'error'); return; }
    api('/api/auth/password', { method: 'POST', body: { oldPassword: o, newPassword: n } })
      .then(function () {
        toast('修改成功，请重新登录', 'success');
        setTimeout(function () { location.href = '/login.html'; }, 800);
      })
      .catch(function (e) { toast(e.message, 'error'); });
  };

  window.logout = function () {
    fetch('/api/auth/logout', { method: 'POST' }).then(function () {
      location.href = '/login.html';
    });
  };

  // 仅对 ISO 日期时间（2026-01-02T15:04:05）把 T 换成空格，普通字符串（如 Top10）不受影响
  var ISO_T_RE = /^(\d{4}-\d{2}-\d{2})[Tt](\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)$/;
  window.fmtCell = function (v) {
    if (v === null || v === undefined) return '';
    if (typeof v === 'string') {
      var m = v.match(ISO_T_RE);
      return m ? m[1] + ' ' + m[2] : v;
    }
    return String(v);
  };

  // ───── LiteTable 轻量表格组件 ─────
  // 统一「表头渲染 + 行渲染 + 加载/空态 + 分页器」路径，替代各页面复制的表格/分页逻辑。
  // 用法:
  //   var t = new LiteTable({
  //     table: '#grid',                 // <table>(含 <thead> 则由 columns 生成表头)
  //     pager: '#pager',                // 分页容器(可选, 需 .pager 样式)
  //     columns: [
  //       {title: '<input ...>', render: function(row, idx){ return '<td>...</td>'; }, noWrap: true},
  //     ],
  //     fetch: function (page, size) { return api(...).then(r => ({rows, total})); },
  //     emptyText: '暂无数据',
  //     onLoaded: function (rows) {}
  //   });
  //   t.reload(1);
  // 列定义两种形态:
  //   {title, td}                -- td 为整格 HTML(自行转义)
  //   {title, field}             -- 按字段取值自动 esc
  window.LiteTable = function (opts) {
    var tableEl = typeof opts.table === 'string' ? $(opts.table) : opts.table;
    var pagerEl = opts.pager ? (typeof opts.pager === 'string' ? $(opts.pager) : opts.pager) : null;
    var totalEl = opts.totalEl ? (typeof opts.totalEl === 'string' ? $(opts.totalEl) : opts.totalEl) : null;
    var sizeOf = opts.sizeOf || function () { return opts.pageSize || 10; };
    var state = { page: 1, size: opts.pageSize || 10, total: 0 };
    var api = {};

    var thead = tableEl.querySelector('thead');
    if (thead && opts.columns) {
      thead.innerHTML = '<tr>' + opts.columns.map(function (c) {
        return '<th' + (c.thAttr || '') + '>' + (c.title || '') + '</th>';
      }).join('') + '</tr>';
    }
    var tbody = tableEl.querySelector('tbody');
    var colspan = function () { return opts.columns.length; };

    function renderPager() {
      if (totalEl) totalEl.textContent = state.total;
      if (!pagerEl) return;
      var pages = Math.max(1, Math.ceil(state.total / state.size));
      var html = '';
      if (!totalEl) html += '<span class="muted" style="margin-right:10px">共 ' + state.total + ' 条数据</span>';
      html += '<span class="pg' + (state.page <= 1 ? ' disabled' : '') + '" data-p="' + (state.page - 1) + '">&lt;</span>';
      var shown = [];
      if (opts.ellipsis && pages > 7) {
        shown.push(1);
        if (state.page > 3) shown.push('…L');
        for (var i = Math.max(2, state.page - 1); i <= Math.min(pages - 1, state.page + 1); i++) shown.push(i);
        if (state.page < pages - 2) shown.push('…R');
        if (pages > 1) shown.push(pages);
      } else {
        for (var j = 1; j <= pages; j++) shown.push(j);
      }
      shown.forEach(function (i) {
        if (i === '…L' || i === '…R') { html += '<span class="pg disabled">…</span>'; return; }
        html += '<span class="pg' + (i === state.page ? ' cur' : '') + '" data-p="' + i + '">' + i + '</span>';
      });
      html += '<span class="pg' + (state.page >= pages ? ' disabled' : '') + '" data-p="' + (state.page + 1) + '">&gt;</span>';
      pagerEl.innerHTML = html;
    }
    if (pagerEl) {
      pagerEl.addEventListener('click', function (e) {
        var t = e.target.closest('.pg');
        if (!t || t.classList.contains('disabled')) return;
        api.reload(parseInt(t.dataset.p));
      });
    }

    api.reload = function (page) {
      state.size = sizeOf() || state.size;
      state.page = page || state.page || 1;
      tbody.innerHTML = '<tr><td colspan="' + colspan() + '" class="empty-tip">加载中...</td></tr>';
      return Promise.resolve(opts.fetch(state.page, state.size)).then(function (r) {
        var rows = r.rows || [];
        state.total = r.total !== undefined ? r.total : rows.length;
        if (!rows.length) {
          tbody.innerHTML = '<tr><td colspan="' + colspan() + '" class="empty-tip">' + esc(opts.emptyText || '暂无数据') + '</td></tr>';
        } else {
          tbody.innerHTML = rows.map(function (row, i) {
            var idx = (state.page - 1) * state.size + i;
            return '<tr>' + opts.columns.map(function (c) {
              if (c.td) return '<td>' + c.td(row, idx) + '</td>';
              var v = row[c.field];
              return '<td>' + esc(v === undefined || v === null ? '' : v) + '</td>';
            }).join('') + '</tr>';
          }).join('');
        }
        renderPager();
        if (opts.onLoaded) opts.onLoaded(rows, r);
      }).catch(function (e) {
        tbody.innerHTML = '<tr><td colspan="' + colspan() + '" class="empty-tip" style="color:#ff4d4f">' +
          esc(e.message || '加载失败') + '</td></tr>';
      });
    };
    api.state = state;
    return api;
  };
})();
