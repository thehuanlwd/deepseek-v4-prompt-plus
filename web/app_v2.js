/* ── DSPlus 前端核心业务与交互逻辑 (YoRHa Terminal v2.0) ── */

let lastMouseDownX = 0;
let lastMouseDownY = 0;
let lastMouseDownTime = 0;

document.addEventListener('mousedown', (e) => {
  lastMouseDownX = e.clientX;
  lastMouseDownY = e.clientY;
  lastMouseDownTime = Date.now();
});

let currentDetailId = null;
let ws = null;
let logs = [];
let currentSessionId = null;
let sessionSummaries = [];
let analysisSessionsOffset = 0;
let analysisSessionsLoading = false;
let analysisSessionsHasMore = true;
let analysisTimelineState = {
  sessionId: null,
  offset: 0,
  limit: 100,
  total: 0,
  loading: false,
  hasMore: false
};

function getSemanticLabel(type) {
  const keyMap = {
    'tool_call': 'semantic.tool_call',
    'callback_call': 'semantic.callback_call',
    'tool_result': 'semantic.tool_result',
    'thinking_cont': 'semantic.thinking_cont',
    'thinking_finished': 'semantic.thinking_finished',
    'antiloop_retry': 'semantic.antiloop_retry',
    'antiloop_analyzer': 'semantic.antiloop_analyzer',
    'antihallucination_retry': 'semantic.antihallucination_retry',
    'debug': 'semantic.debug',
    'chat': 'semantic.chat'
  };
  const k = keyMap[type];
  if (k && typeof t === 'function') {
    const translated = t(k);
    if (translated && translated !== k) return translated;
  }
  // 兜底（初始加载前）
  const fallbacks = {
    'tool_call': '工具调用',
    'callback_call': '回传调用',
    'tool_result': '工具回传',
    'thinking_cont': '继续思考',
    'thinking_finished': '思考完成',
    'antiloop_retry': '防无限思考重试',
    'antiloop_analyzer': '防无限思考分析',
    'antihallucination_retry': '思维修正',
    'debug': 'Debug',
    'chat': '对话'
  };
  return fallbacks[type] || type;
}

// 语言切换后供 i18n 模块调用的动态重渲染钩子
window.refreshI18nDynamic = function () {
  try {
    if (typeof renderLogs === 'function') renderLogs();

    // 诊断分析页面动态内容重渲染
    const analysisPage = document.getElementById('analysis');
    if (analysisPage && analysisPage.classList.contains('active')) {
      if (typeof renderSessionList === 'function') renderSessionList();
      // 语言切换时重新渲染详情（会重新拉取但保证所有标签更新）
      if (currentSessionId && typeof showSessionDetail === 'function') {
        // 避免重复加载冲突，延迟一点
        setTimeout(() => { if (currentSessionId) showSessionDetail(currentSessionId); }, 10);
      }
    }

    // 设置页面：自定义下拉组件需要重新构建（因为它在 convert 时从 option.textContent 拷贝了文本）
    // 语言切换后 <option> 已被 applyI18n 更新，但自定义 UI 还没刷新
    const settingsPage = document.getElementById('settings');
    if (settingsPage && settingsPage.classList.contains('active') && typeof convertSelectsToYorha === 'function') {
      // 延迟一点确保 applyI18n 完成更新 option 文本
      setTimeout(() => {
        convertSelectsToYorha();
        // 重新触发 change 以更新子分组显示（如 effortGroup）
        const selects = document.querySelectorAll('#settings select, #settings input[type=checkbox]');
        selects.forEach(el => el.dispatchEvent(new Event('change')));
      }, 10);
    }
  } catch (e) {}
};

function $(id) { return document.getElementById(id); }

function formatCacheRatio(hit, miss) {
  const total = hit + miss;
  if (total <= 0) return '0%';
  if (miss === 0) return '100%';
  if (hit === 0) return '0%';
  const pct = (hit / total) * 100;
  if (pct > 99.9) return '99.9%';
  if (pct < 0.1) return '0.1%';
  return pct.toFixed(1) + '%';
}

// ── 应用主题 ──
function applyTheme() {
  const theme = localStorage.getItem('dsplus_theme') || 'yorha';
  document.body.className = 'theme-' + theme;
  const cfgTheme = $('cfgTheme');
  if (cfgTheme) {
    cfgTheme.value = theme;
  }
}

// ── 初始化 ──
function init() {
  // 语言系统初始化（加载翻译 + 应用）
  let i18nPromise = Promise.resolve();
  if (typeof initI18n === 'function') {
    i18nPromise = initI18n().then(() => {
      if (typeof applyI18n === 'function') applyI18n();
    });
  }

  // 初始化主题外观
  applyTheme();

  // 导航栏切换 (YoRHa 标签式切换)
  document.querySelectorAll('.yorha-nav button').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.yorha-nav button').forEach(b => b.classList.remove('active-tab'));
      btn.classList.add('active-tab');
      document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
      
      const pageId = btn.dataset.page;
      $(pageId).classList.add('active');
      
      if (pageId === 'settings') loadSettings();
      if (pageId === 'analysis') loadAnalysisSessions();
      if (pageId === 'models') initModelServicesPage();
      
      // 切页时自动收拢抽屉
      if (pageId !== 'dashboard') {
        closeDrawer();
      }
    });
  });

  $('btnClear').addEventListener('click', clearLogs);
  $('searchInput').addEventListener('input', renderLogs);
  
  // 设置控件关联联动
  $('cfgThinkingMode').addEventListener('change', () => {
    $('effortGroup').style.display = $('cfgThinkingMode').value === 'enabled' ? '' : 'none';
  });
  $('cfgExtraPlacement').addEventListener('change', () => {
    $('extraPromptGroup').style.display = $('cfgExtraPlacement').value === 'none' ? 'none' : '';
  });
  $('cfgMaxTokensMode').addEventListener('change', () => {
    $('maxTokensCustomGroup').style.display = $('cfgMaxTokensMode').value === 'custom' ? '' : 'none';
  });
  $('cfgAntiLoop').addEventListener('change', () => {
    $('antiloopSubSettings').style.display = $('cfgAntiLoop').checked ? '' : 'none';
  });
  $('cfgAntiHallucination').addEventListener('change', () => {
    $('antihallucinationSubSettings').style.display = $('cfgAntiHallucination').checked ? '' : 'none';
  });

  $('cfgAntiloopRetryThinking').addEventListener('change', () => {
    $('antiloopEffortGroup').style.display = $('cfgAntiloopRetryThinking').value === 'enabled' ? '' : 'none';
  });
  $('cfgAnalysisEnabled').addEventListener('change', () => {
    $('analysisSubSettings').style.display = $('cfgAnalysisEnabled').checked ? '' : 'none';
  });

  // 主题实时切换监听
  const cfgTheme = $('cfgTheme');
  if (cfgTheme) {
    cfgTheme.addEventListener('change', () => {
      const selectedTheme = cfgTheme.value;
      document.body.className = 'theme-' + selectedTheme;
    });
  }

  // 抽屉外部点击收拢
  document.addEventListener('click', function (e) {
    const drawer = $('yorhaDrawer');
    if (drawer && drawer.classList.contains('open')) {
      if (!drawer.contains(e.target) && !e.target.closest('.yorha-table tbody tr')) {
        closeDrawer();
      }
    }
  });

  const sessionListContainer = $('sessionListContainer');
  if (sessionListContainer) {
    sessionListContainer.addEventListener('scroll', () => {
      const { scrollTop, scrollHeight, clientHeight } = sessionListContainer;
      if (scrollHeight - scrollTop - clientHeight < 50) {
        loadNextAnalysisSessions();
      }
    });
  }

  connectWS();
  loadStatus();

  // 初始加载日志（确保列表有数据）
  // 注意：loadLogs 主要在启动 .then 中调用，这里也触发一次以防万一
  if (typeof loadLogs === 'function') {
    // 延迟一点确保 WS 也连上
    setTimeout(() => loadLogs(), 100);
  }

  return i18nPromise;
}

// ── 加载状态 ──
function formatIntCompact(n) {
  n = n || 0;
  if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(n);
}
function formatRate(r) {
  r = r || 0;
  if (r >= 1000) return (r / 1000).toFixed(1) + 'k/s';
  return r.toFixed(1) + '/s';
}
async function loadStatus() {
  try {
    const r = await fetch('/api/status');
    const s = await r.json();
    $('statPort').textContent = s.port;
    $('statTotal').textContent = s.total;
    $('statTotalDashboard').textContent = s.total;
    $('emptyPort').textContent = formatRate(s.tokens_rate);

    const rateDescEl = document.querySelector('#emptyPort') ? document.querySelector('#emptyPort').parentElement.querySelector('.figure-desc') : null;
    if (rateDescEl) {
      rateDescEl.title = '流式请求解码阶段加权平均速率 (总有效输出Tokens / 总有效生成秒数)';
    }

    if (s.total_tokens > 0) {
      $('statToday').textContent = formatIntCompact(s.total_tokens);
      const totalPrompt = (s.cache_hit_tokens || 0) + (s.cache_miss_tokens || 0);
      const hitRate = totalPrompt > 0 ? (((s.cache_hit_tokens || 0) / totalPrompt) * 100).toFixed(1) + '%' : '0%';
      const descEl = document.querySelector('#statToday') ? document.querySelector('#statToday').parentElement.querySelector('.figure-desc') : null;
      if (descEl) {
        descEl.textContent = `入:${formatIntCompact(s.prompt_tokens || 0)} (缓存:${hitRate}) | 出:${formatIntCompact(s.completion_tokens || 0)}`;
        descEl.title = `总Tokens: ${s.total_tokens.toLocaleString()}\n输入: ${(s.prompt_tokens || 0).toLocaleString()} (命中: ${(s.cache_hit_tokens || 0).toLocaleString()}, 未命中: ${(s.cache_miss_tokens || 0).toLocaleString()}, 缓存率: ${hitRate})\n输出: ${(s.completion_tokens || 0).toLocaleString()}`;
      }
    } else {
      $('statToday').textContent = '0';
    }
  } catch(e) {}
}

// ── 加载日志 ──
async function loadLogs() {
  try {
    const r = await fetch('/api/logs?limit=200');
    const data = await r.json();
    logs = data.entries || [];
    renderLogs();
    $('statTotal').textContent = data.total;
    $('statTotalDashboard').textContent = data.total;
    loadStatus();
  } catch(e) {}
}

// ── 数值滚动的 Ease-Out 缓动动画 ──
function animateValue(element, start, end, duration = 150) {
  if (start === end) return;
  // 若当前元素有进行中的缓动，先取消以避免多动画竞态覆盖
  if (element._animRafId) {
    cancelAnimationFrame(element._animRafId);
    element._animRafId = null;
  }
  const startTime = performance.now();
  
  function update(currentTime) {
    const elapsed = currentTime - startTime;
    const progress = Math.min(elapsed / duration, 1);
    const current = Math.floor(start + (end - start) * progress);
    element.textContent = current;
    
    if (progress < 1) {
      element._animRafId = requestAnimationFrame(update);
    } else {
      element._animRafId = null;
    }
  }
  element._animRafId = requestAnimationFrame(update);
}

// ── 格式化 Badges 结构 ──
function getFormatBadgeHtml(e) {
  if (e.format === 'anthropic') {
    return '<span class="badge badge-anthropic">anthropic</span>';
  } else if (e.format === 'openai') {
    return '<span class="badge badge-openai">OpenAI</span>';
  } else {
    return '<span class="badge badge-unknown">' + (e.format||'?') + '</span>';
  }
}

function getSemanticBadgeHtml(type) {
  switch (type) {
    case '对话开始':
      return '<span class="badge badge-chat">对话开始</span>';
    case '工具调用':
      return '<span class="badge badge-tool-call">工具调用</span>';
    case '工具回传':
      return '<span class="badge badge-tool-result">工具回传</span>';
    case '调用回传':
      return '<span class="badge badge-callback-call">调用回传</span>';
    case '完成对话':
      return '<span class="badge badge-yes">完成对话</span>';
    default:
      if (type === 'chat') return '<span class="badge badge-chat">对话开始</span>';
      if (type === 'tool_call') return '<span class="badge badge-tool-call">工具调用</span>';
      if (type === 'tool_result') return '<span class="badge badge-tool-result">工具回传</span>';
      return '<span class="badge badge-unknown">' + (type || '--') + '</span>';
  }
}

// 特殊事件徽章：防无限思考/防幻觉/debug/思考完成 统一收敛到此处，不混淆格式与语义列
function getSpecialBadgeHtml(e) {
  var rt = e.request_type || '';
  if (rt === 'antiloop_analyzer') {
    return '<span class="badge badge-antiloop-analyzer">防无限思考分析</span>';
  } else if (rt === 'antiloop_retry') {
    return '<span class="badge badge-antiloop-retry">防无限思考重试</span>';
  } else if (rt === 'antihallucination_retry') {
    return '<span class="badge badge-antihallucination">思维修正</span>';
  } else if (rt === 'debug') {
    return '<span class="badge badge-debug">Debug</span>';
  }
  // thinking_finished 通过 system_event 标识（防幻觉拦截后更新）
  if (e.system_event === 'thinking_finished') {
    return '<span class="badge badge-thinking-finished">思考完成</span>';
  }
  return '<span style="color:var(--yorha-fg-dim);font-size:10px;">--</span>';
}

// 端点路径展示：特殊类型已在最后列有徽章，API端点列不再显示冗余的 POST/路径
function getPathText(e) {
  var rt = e.request_type || '';
  if (rt === 'debug') {
    return '<span style="color:var(--yorha-fg-dim);font-size:10px;">--</span>';
  }
  return (e.method || 'POST') + ' ' + (e.path || '');
}

function getCacheHtml(e) {
  if (e.token_usage && e.token_usage.total > 0) {
    const u = e.token_usage;
    const promptTotal = u.cache_hit + u.cache_miss;
    if (u.cache_hit === 0 && promptTotal > 0) {
      return `<td class="cache-td"><span class="tokens-new tooltip-wrap">new<span class="tt">无缓存命中（全新上下文）</span></span></td>`;
    } else if (promptTotal > 0) {
      const pct = u.cache_hit / promptTotal * 100;
      const cls = pct >= 75 ? 'tokens-high' : pct >= 35 ? 'tokens-mid' : 'tokens-low';
      const ratioStr = formatCacheRatio(u.cache_hit, u.cache_miss);
      return `<td class="cache-td"><span class="tooltip-wrap ${cls}">${ratioStr}<span class="tt">命中: ${u.cache_hit} | 未命中: ${u.cache_miss}</span></span></td>`;
    } else {
      return `<td class="cache-td"><span class="tokens-new tooltip-wrap">new<span class="tt">无缓存（新的上下文）</span></span></td>`;
    }
  }
  return `<td class="cache-td" style="color:var(--yorha-fg-dim)">--</td>`;
}

function getLatencyGridHtml(e) {
  let latencyGridHtml = '';
  if (e.latency_ms > 0) {
    const blocksCount = Math.min(5, Math.ceil(e.latency_ms / 1000));
    let latencyCls = 'latency-low';
    if (blocksCount === 3 || blocksCount === 4) {
      latencyCls = 'latency-mid';
    } else if (blocksCount === 5) {
      latencyCls = 'latency-high';
    }

    latencyGridHtml = `<div class="latency-grid ${latencyCls}">`;
    for (let i = 1; i <= 5; i++) {
      const activeCls = i <= blocksCount ? ' active' : '';
      latencyGridHtml += `<div class="latency-block${activeCls}"></div>`;
    }
    latencyGridHtml += '</div>';
  }
  return latencyGridHtml;
}

// ── 局部更新 Token Cell (含 Loading、数字变化翻滚、临时暂停状态展示) ──
function updateTokenCell(cell, e) {
  const usage = e.token_usage;
  const status = e.status || "completed";

  if (!usage || usage.total <= 0) {
    const loadingHtml = `<span class="status-loading">Loading...</span>`;
    if (status === "connecting") {
      if (cell.innerHTML !== loadingHtml) cell.innerHTML = loadingHtml;
    } else {
      if (cell.innerHTML !== '--') {
        cell.innerHTML = `--`;
        cell.style.color = "var(--yorha-fg-dim)";
      }
    }
    return;
  }

  let curStatus = status;
  if (curStatus === "connecting") curStatus = "streaming";

  const span = cell.querySelector('.roll-number');
  const ttSpan = cell.querySelector('.tt');
  const oldVal = span ? parseInt(span.textContent) || 0 : 0;
  const newVal = usage.total;
  const tips = `输入: ${usage.prompt} | 输出: ${usage.completion} | 命中: ${usage.cache_hit} | 未命中: ${usage.cache_miss}`;

  if (curStatus === "streaming") {
    if (!span || !ttSpan) {
      cell.innerHTML = `<span class="tooltip-wrap"><span class="roll-number">${oldVal}</span><span class="tt">${tips}</span></span>`;
      const numSpan = cell.querySelector('.roll-number');
      animateValue(numSpan, oldVal, newVal);
    } else {
      if (ttSpan.innerHTML !== tips) ttSpan.innerHTML = tips;
      if (oldVal !== newVal) {
        animateValue(span, oldVal, newVal);
      }
    }
  } else if (curStatus === "paused") {
    const hasPausedClass = cell.querySelector('.status-paused');
    if (!span || !ttSpan || !hasPausedClass) {
      cell.innerHTML = `<span class="status-paused tooltip-wrap"><span class="roll-number">${oldVal}</span>...<span class="tt">${tips}</span></span>`;
      const numSpan = cell.querySelector('.roll-number');
      animateValue(numSpan, oldVal, newVal);
    } else {
      if (ttSpan.innerHTML !== tips) ttSpan.innerHTML = tips;
      if (oldVal !== newVal) {
        animateValue(span, oldVal, newVal);
      }
    }
  } else {
    const finalHtml = `<span class="tooltip-wrap">${newVal}<span class="tt">${tips}</span></span>`;
    if (cell.innerHTML !== finalHtml) {
      cell.innerHTML = finalHtml;
    }
  }
}

function updateRowCells(tr, e, time, semBadge, fmtBadge, streamBadge, tfBadge, scClass, scText, latencyGridHtml, latencyStr, totalMsStr, cacheHtml, specialBadge) {
  const tds = tr.children;
  if (tds.length >= 12) {
    if (tds[0].innerHTML !== time) tds[0].innerHTML = time;
    if (tds[1].innerHTML !== semBadge) tds[1].innerHTML = semBadge;
    if (tds[2].innerHTML !== fmtBadge) tds[2].innerHTML = fmtBadge;
    if (tds[3].innerHTML !== streamBadge) tds[3].innerHTML = streamBadge;
    if (tds[4].innerHTML !== tfBadge) tds[4].innerHTML = tfBadge;

    if (tds[5].innerHTML !== scText) {
      tds[5].innerHTML = scText;
      tds[5].className = scClass;
    }

    const latencyContent = `${latencyGridHtml}<span class="latency">${latencyStr}</span>`;
    if (tds[6].innerHTML !== latencyContent) {
      tds[6].innerHTML = latencyContent;
    }

    if (tds[7].innerHTML !== totalMsStr) {
      tds[7].innerHTML = totalMsStr;
    }

    updateTokenCell(tds[8], e);

    // 缓存列替换
    const tempTable = document.createElement('table');
    tempTable.innerHTML = `<tr>${cacheHtml}</tr>`;
    const newCacheTd = tempTable.querySelector('td');
    if (tds[9].innerHTML !== newCacheTd.innerHTML) {
      tds[9].innerHTML = newCacheTd.innerHTML;
      tds[9].className = newCacheTd.className;
    }

    const pathContent = getPathText(e);
    if (tds[10].innerHTML !== pathContent) {
      tds[10].innerHTML = pathContent;
    }

    // 特殊事件列：防无限思考/防幻觉/debug
    if (tds[11].innerHTML !== specialBadge) {
      tds[11].innerHTML = specialBadge;
    }
  }
}

function matchesLogQuery(e, query) {
  if (!query) return true;
  const semLabel = getSemanticLabel(e.semantic_type) || '对话';
  return (e.path||'').toLowerCase().includes(query) ||
         (e.format||'').toLowerCase().includes(query) ||
         (e.semantic_type||'').toLowerCase().includes(query) ||
         semLabel.toLowerCase().includes(query) ||
         String(e.status_code).includes(query);
}

function updateSingleRow(tr, e) {
  const time = new Date(e.time).toLocaleTimeString('zh-CN', {hour:'2-digit',minute:'2-digit',second:'2-digit'});
  const fmtBadge = getFormatBadgeHtml(e);
  const streamBadge = e.stream ? '<span class="badge badge-yes">YES</span>' : '<span class="badge badge-no">NO</span>';
  const tfBadge = e.transformed ? '<span class="badge badge-yes">是</span>' : '<span class="badge badge-no">否</span>';
  const semBadge = getSemanticBadgeHtml(e.semantic_type);
  const specialBadge = getSpecialBadgeHtml(e);

  const scClass = e.status_code === 0 ? 'status-pending' : (e.status_code >= 400 ? 'status-400' : e.status_code >= 200 && e.status_code < 300 ? 'status-200' : '');
  const scText = e.status_code === 0 ? 'PENDING' : e.status_code;
  const isPending = e.status_code === 0 || e.latency_ms === 0;
  const latencyStr = isPending ? '--' : (e.latency_ms >= 1000 ? (e.latency_ms/1000).toFixed(1)+'s' : e.latency_ms+'ms');
  const totalMsStr = (!e.total_ms || e.total_ms === 0) ? '--' : (e.total_ms >= 1000 ? (e.total_ms/1000).toFixed(1)+'s' : e.total_ms+'ms');

  const cacheHtml = getCacheHtml(e);
  const latencyGridHtml = getLatencyGridHtml(e);

  updateRowCells(tr, e, time, semBadge, fmtBadge, streamBadge, tfBadge, scClass, scText, latencyGridHtml, latencyStr, totalMsStr, cacheHtml, specialBadge);
}

function buildRowElement(e) {
  const time = new Date(e.time).toLocaleTimeString('zh-CN', {hour:'2-digit',minute:'2-digit',second:'2-digit'});
  const fmtBadge = getFormatBadgeHtml(e);
  const streamBadge = e.stream ? '<span class="badge badge-yes">YES</span>' : '<span class="badge badge-no">NO</span>';
  const tfBadge = e.transformed ? '<span class="badge badge-yes">是</span>' : '<span class="badge badge-no">否</span>';
  const semBadge = getSemanticBadgeHtml(e.semantic_type);
  const specialBadge = getSpecialBadgeHtml(e);

  const scClass = e.status_code === 0 ? 'status-pending' : (e.status_code >= 400 ? 'status-400' : e.status_code >= 200 && e.status_code < 300 ? 'status-200' : '');
  const scText = e.status_code === 0 ? 'PENDING' : e.status_code;
  const isPending = e.status_code === 0 || e.latency_ms === 0;
  const latencyStr = isPending ? '--' : (e.latency_ms >= 1000 ? (e.latency_ms/1000).toFixed(1)+'s' : e.latency_ms+'ms');
  const totalMsStr = (!e.total_ms || e.total_ms === 0) ? '--' : (e.total_ms >= 1000 ? (e.total_ms/1000).toFixed(1)+'s' : e.total_ms+'ms');

  const cacheHtml = getCacheHtml(e);
  const latencyGridHtml = getLatencyGridHtml(e);

  const tr = document.createElement('tr');
  tr.dataset.id = e.id;
  tr.addEventListener('click', () => showDetail(e.id));
  
  tr.innerHTML = `
    <td>${time}</td>
    <td>${semBadge}</td>
    <td>${fmtBadge}</td>
    <td>${streamBadge}</td>
    <td>${tfBadge}</td>
    <td class="${scClass}">${scText}</td>
    <td>${latencyGridHtml}<span class="latency">${latencyStr}</span></td>
    <td class="total-td" style="color:var(--yorha-fg-dim)">${totalMsStr}</td>
    <td class="tokens-td"></td>
    ${cacheHtml}
    <td class="endpoint-cell" style="color:var(--yorha-fg-dim)">${getPathText(e)}</td>
    <td>${specialBadge}</td>
  `;
  updateTokenCell(tr.querySelector('.tokens-td'), e);
  if (e.id === currentDetailId) {
    tr.classList.add('selected-row');
  }
  return tr;
}

// ── 渲染日志列表 ──
function renderLogs() {
  const query = $('searchInput').value.toLowerCase();
  const filtered = logs.filter(e => matchesLogQuery(e, query));
  const body = $('logBody');

  if (filtered.length === 0) {
    body.innerHTML = '';
    if (logs.length === 0) {
      $('logTable').style.display = 'none';
      $('emptyState').style.display = '';
    } else {
      $('logTable').style.display = '';
      $('emptyState').style.display = 'none';
      body.innerHTML = '<tr><td colspan="12" style="text-align:center;color:var(--yorha-fg-dim);padding:20px">' + (t('common.no_match') || '无匹配记录') + '</td></tr>';
    }
    return;
  }

  $('logTable').style.display = '';
  $('emptyState').style.display = 'none';

  const existingRows = {};
  body.querySelectorAll('tr[data-id]').forEach(tr => {
    existingRows[tr.dataset.id] = tr;
  });

  const fragment = document.createDocumentFragment();
  filtered.forEach(e => {
    let tr = existingRows[e.id];
    if (tr) {
      updateSingleRow(tr, e);
    } else {
      tr = buildRowElement(e);
    }

    if (e.id === currentDetailId) {
      tr.classList.add('selected-row');
    } else {
      tr.classList.remove('selected-row');
    }

    fragment.appendChild(tr);
  });

  body.innerHTML = '';
  body.appendChild(fragment);
}

const MAX_PREVIEW_BYTES = 200 * 1024; // 200KB 安全预览阈值

function updateDrawerHeaderInfo(e) {
  if (!$('yorhaDrawer') || !$('yorhaDrawer').classList.contains('open')) return;
  const time = new Date(e.time).toLocaleString('zh-CN');
  const rtLabel = e.request_type === 'antiloop_analyzer' ? ' (防无限思考分析器)' :
                  e.request_type === 'antiloop_retry' ? ' (防无限思考重试)' : '';
  const isPending = e.status_code === 0 || e.latency_ms === 0;
  const latencyStr = isPending ? '等待首响...' : (e.latency_ms >= 1000 ? (e.latency_ms/1000).toFixed(1)+'s' : e.latency_ms+'ms');
  const totalStr = (!e.total_ms || e.total_ms === 0) ? '--' : (e.total_ms >= 1000 ? (e.total_ms/1000).toFixed(1)+'s' : e.total_ms+'ms');

  let info = `时间: ${time}
格式: ${e.format}${rtLabel}
流式: ${e.stream ? '是' : '否'}
方法: ${e.method} ${e.path}
状态码: ${e.status_code === 0 ? 'PENDING' : e.status_code}
延迟: ${latencyStr}
总耗时: ${totalStr}
是否重组: ${e.transformed ? '是' : '否'}
包含System Prompt: ${e.has_system_prompt ? '是' : '否'}`;
  $('drawer-info').textContent = info;
}

function downloadRawText(text, filename) {
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

function renderSafeText(section, rawText, defaultFileName = 'data.txt') {
  const contentEl = section.querySelector('.detail-content');
  if (!contentEl) return;
  if (!rawText) {
    contentEl.textContent = '--';
    return;
  }

  // 清除旧下载按钮
  const oldBtn = section.querySelector('.btn-download-full');
  if (oldBtn) oldBtn.remove();

  if (rawText.length > MAX_PREVIEW_BYTES) {
    const half = Math.floor(MAX_PREVIEW_BYTES / 2); // 100KB 前后对半
    const head = rawText.slice(0, half);
    const tail = rawText.slice(rawText.length - half);
    const omittedKb = Math.round((rawText.length - MAX_PREVIEW_BYTES) / 1024);
    const totalMb = (rawText.length / 1024 / 1024).toFixed(2);
    contentEl.textContent = head + `\n\n... [中间已安全省略约 ${omittedKb} KB 内容，完整文件共 ${totalMb} MB，可点击右上方按钮下载完整内容] ...\n\n` + tail;

    const dlBtn = document.createElement('button');
    dlBtn.className = 'button button-accent btn-download-full';
    dlBtn.style.marginLeft = '8px';
    dlBtn.style.padding = '2px 8px';
    dlBtn.style.fontSize = '11px';
    dlBtn.textContent = '下载完整内容 (' + totalMb + ' MB)';
    dlBtn.onclick = (evt) => {
      evt.stopPropagation();
      downloadRawText(rawText, defaultFileName);
    };
    const header = section.querySelector('.detail-header') || section.firstElementChild;
    if (header) header.appendChild(dlBtn);
  } else {
    if (section.id === 'sec-rawRequest' || section.id === 'sec-mutatedRequest') {
      contentEl.textContent = tryPretty(rawText);
    } else {
      contentEl.textContent = prettyBody(rawText);
    }
  }
}

// ── 右侧滑动抽屉详情展示 ──
async function showDetail(id) {
  currentDetailId = id;
  
  // 更新表格中高亮行
  document.querySelectorAll('.yorha-table tbody tr').forEach(tr => {
    if (tr.dataset.id == id) {
      tr.classList.add('selected-row');
    } else {
      tr.classList.remove('selected-row');
    }
  });

  try {
    const r = await fetch('/api/logs/' + id);
    const e = await r.json();
    window._currentDrawerDetail = e; // 全局缓存

    $('drawerTitle').textContent = 'DATA TRACE #' + id;

    // 1. 基本信息
    updateDrawerHeaderInfo(e);

    // 2. 响应头
    $('sec-headers').style.display = (e.response_headers && Object.keys(e.response_headers).length > 0) ? '' : 'none';

    // 3. 原始请求体
    $('sec-rawRequest').style.display = e.original_body ? '' : 'none';

    // 4. 重组后请求体
    $('sec-mutatedRequest').style.display = e.transformed_body ? '' : 'none';

    // 5. 上游原始响应（仅 anti-loop 模式下有值）
    $('sec-upstreamResponse').style.display = e.upstream_response_body ? '' : 'none';

    // 6. API响应体与最终回复（延迟到真正点击展开时解析，绝不阻塞主线程）
    if (e.response_body) {
      $('sec-apiResponse').style.display = '';
      $('sec-finalReply').style.display = '';
    } else {
      $('sec-apiResponse').style.display = 'none';
      $('sec-finalReply').style.display = 'none';
    }

    if (!e.original_body && !e.transformed_body && !e.response_body) {
      $('drawer-info').textContent += '\n\n提示: 请开启"详细记录"模式以查看完整请求与响应内容。';
    }

    // 默认折叠抽屉中所有段落并清空内容区（除基本信息外）
    document.querySelectorAll('.yorha-drawer-body .detail-section').forEach(sec => {
      sec.classList.add('collapsed');
      const content = sec.querySelector('.detail-content');
      if (content && sec.id !== 'sec-info') {
        content.textContent = '';
      }
      const dlBtn = sec.querySelector('.btn-download-full');
      if (dlBtn) dlBtn.remove();
    });

    $('yorhaDrawer').classList.add('open');
  } catch(err) {
    console.error(err);
  }
}

function closeDrawer() {
  const drawer = $('yorhaDrawer');
  if (drawer) {
    drawer.classList.remove('open');
  }
  currentDetailId = null;
  document.querySelectorAll('.yorha-table tbody tr').forEach(tr => tr.classList.remove('selected-row'));
  
  // 清理 DOM 内容以释放大对象内存
  document.querySelectorAll('.yorha-drawer-body .detail-section').forEach(sec => {
    sec.classList.add('collapsed');
    const content = sec.querySelector('.detail-content');
    if (content && sec.id !== 'sec-info') {
      content.textContent = '';
    }
    const dlBtn = sec.querySelector('.btn-download-full');
    if (dlBtn) dlBtn.remove();
  });
  window._currentDrawerDetail = null;
}

// ── 折叠块切换（懒加载真折叠） ──
function toggleDetailSection(label) {
  const section = label.parentElement;
  const wasCollapsed = section.classList.contains('collapsed');
  
  if (wasCollapsed) {
    section.classList.remove('collapsed');
    const contentEl = section.querySelector('.detail-content');
    if (contentEl) {
      const e = window._currentDrawerDetail;
      if (e) {
        if (section.id === 'sec-headers') {
          contentEl.textContent = Object.entries(e.response_headers || {}).map(([k,v]) => `${k}: ${v}`).join('\n');
        } else if (section.id === 'sec-rawRequest') {
          renderSafeText(section, e.original_body, `raw_request_${e.id}.json`);
        } else if (section.id === 'sec-mutatedRequest') {
          renderSafeText(section, e.transformed_body, `mutated_request_${e.id}.json`);
        } else if (section.id === 'sec-upstreamResponse') {
          renderSafeText(section, e.upstream_response_body, `upstream_response_${e.id}.txt`);
        } else if (section.id === 'sec-apiResponse') {
          renderSafeText(section, e.response_body, `api_response_${e.id}.txt`);
        } else if (section.id === 'sec-finalReply') {
          contentEl.innerHTML = renderFinalReply(e.response_body, e.format);
        }
      }
    }
  } else {
    section.classList.add('collapsed');
    if (section.id !== 'sec-info') {
      const contentEl = section.querySelector('.detail-content');
      if (contentEl) {
        contentEl.textContent = '';
      }
      const dlBtn = section.querySelector('.btn-download-full');
      if (dlBtn) dlBtn.remove();
    }
  }
}

// ── 抽屉段落文本复制 ──
function copyDetailText(btn) {
  const content = btn.parentElement.querySelector('.detail-content').textContent;
  navigator.clipboard.writeText(content).then(() => {
    btn.textContent = '已复制';
    setTimeout(() => { btn.textContent = '复制'; }, 1500);
  }).catch(() => {
    btn.textContent = '失败';
    setTimeout(() => { btn.textContent = '复制'; }, 1500);
  });
}

function tryPretty(str) {
  try {
    return JSON.stringify(JSON.parse(str), null, 2);
  } catch(e) {
    return str;
  }
}

// prettySSE 对 SSE 流文本做行级 JSON 美化：每行 data: 后的 JSON 单独格式化。
function prettySSE(str) {
  if (!str || !str.includes('data: ')) return str;
  return str.split('\n').map(line => {
    const trimmed = line.trim();
    if (!trimmed.startsWith('data: ')) return line;
    const dataStr = trimmed.slice(6);
    if (dataStr === '[DONE]' || dataStr === '') return line;
    try {
      const obj = JSON.parse(dataStr);
      return 'data: ' + JSON.stringify(obj, null, 2);
    } catch (e) {
      return line; // 该行非合法 JSON，原样保留
    }
  }).join('\n');
}

// 根据内容选择美化方式：SSE 流用行级美化，单 JSON 用整体美化
function prettyBody(str) {
  if (!str) return str;
  if (str.includes('data: ')) return prettySSE(str);
  return tryPretty(str);
}

// parseFinalReply 从原始响应体提取分段：reasoning / toolCalls / content
// 返回 {reasoning, toolCalls, content}，toolCalls 为 [{id,name,input}] 结构化数组
function parseFinalReply(rawText, format) {
  const result = { reasoning: "", toolCalls: [], content: "" };
  if (!rawText) return result;

  if (rawText.includes("data: ")) {
    // 流式
    const openaiBuilders = {}; // index -> {id,name,args}
    const anthropicBuilders = {}; // index -> {id,name,input}
    const lines = rawText.split("\n");
    for (let line of lines) {
      line = line.trim();
      if (!line.startsWith("data: ")) continue;
      const dataStr = line.slice(6);
      if (dataStr === "[DONE]") continue;
      try {
        const chunk = JSON.parse(dataStr);
        // OpenAI choices 格式
        if (chunk.choices && chunk.choices.length > 0) {
          const delta = chunk.choices[0].delta;
          if (delta) {
            if (delta.reasoning_content) result.reasoning += delta.reasoning_content;
            if (delta.content) result.content += delta.content;
            // OpenAI 流式 tool_calls
            if (Array.isArray(delta.tool_calls)) {
              for (const tc of delta.tool_calls) {
                const idx = tc.index || 0;
                if (!openaiBuilders[idx]) openaiBuilders[idx] = { id: '', name: '', args: '' };
                if (tc.id) openaiBuilders[idx].id = tc.id;
                if (tc.function) {
                  if (tc.function.name) openaiBuilders[idx].name = tc.function.name;
                  if (tc.function.arguments) openaiBuilders[idx].args += tc.function.arguments;
                }
              }
            }
          }
        }
        // Anthropic content_block_delta
        if (chunk.type === "content_block_delta" && chunk.delta) {
          if (chunk.delta.thinking) result.reasoning += chunk.delta.thinking;
          if (chunk.delta.text) result.content += chunk.delta.text;
          if (chunk.delta.type === "input_json_delta" && chunk.delta.partial_json) {
            const idx = chunk.index || 0;
            if (anthropicBuilders[idx]) anthropicBuilders[idx].input += chunk.delta.partial_json;
          }
        }
        // Anthropic content_block_start (tool_use)
        if (chunk.type === "content_block_start" && chunk.content_block && chunk.content_block.type === "tool_use") {
          const idx = chunk.index || 0;
          anthropicBuilders[idx] = {
            id: chunk.content_block.id || '',
            name: chunk.content_block.name || '',
            input: ''
          };
        }
      } catch (err) {}
    }
    // 收集 OpenAI tool_calls
    for (const idx of Object.keys(openaiBuilders).sort((a,b)=>a-b)) {
      const b = openaiBuilders[idx];
      result.toolCalls.push({ id: b.id, name: b.name, input: b.args });
    }
    // 收集 Anthropic tool_calls
    for (const idx of Object.keys(anthropicBuilders).sort((a,b)=>a-b)) {
      const b = anthropicBuilders[idx];
      result.toolCalls.push({ id: b.id, name: b.name, input: b.input });
    }
  } else {
    // 非流式
    try {
      const data = JSON.parse(rawText);
      if (data.choices && data.choices.length > 0) {
        const msg = data.choices[0].message;
        if (msg) {
          if (msg.reasoning_content) result.reasoning = msg.reasoning_content;
          if (msg.content) result.content = msg.content;
          // OpenAI 非流式 tool_calls
          if (Array.isArray(msg.tool_calls)) {
            for (const tc of msg.tool_calls) {
              if (tc.function) {
                result.toolCalls.push({ id: tc.id || '', name: tc.function.name || '', input: tc.function.arguments || '' });
              }
            }
          }
        }
      }
      // Anthropic 非流式 content 数组
      if (Array.isArray(data.content)) {
        for (const item of data.content) {
          if (item.type === "text" && item.text) result.content += item.text;
          if (item.type === "thinking" && item.thinking) result.reasoning += item.thinking;
          if (item.type === "tool_use") {
            result.toolCalls.push({ id: item.id || '', name: item.name || '', input: JSON.stringify(item.input || {}) });
          }
        }
      }
    } catch (err) {}
  }
  return result;
}

// renderFinalReply 把 parseFinalReply 的分段结果渲染为 HTML
function renderFinalReply(rawText, format) {
  const r = parseFinalReply(rawText, format);
  let html = '';
  if (r.reasoning) {
    html += `<div style="margin-bottom:8px;"><div style="color:var(--yorha-fg-dim); font-size:11px; margin-bottom:2px;">[思考]</div><div style="white-space:pre-wrap; color:var(--yorha-fg-dim);">&lt;think&gt;\n${escapeHtml(r.reasoning)}\n&lt;/think&gt;</div></div>`;
  }
  if (r.toolCalls && r.toolCalls.length > 0) {
    const tcHtml = r.toolCalls.map(tc => {
      let inputStr = tc.input || '';
      try { inputStr = JSON.stringify(JSON.parse(tc.input), null, 2); } catch(e) {}
      return `<div style="margin: 4px 0; padding: 4px 6px; background: rgba(0,0,0,0.05); border-left: 2px solid var(--yorha-accent);"><span style="color:var(--yorha-accent); font-weight:600;">${escapeHtml(tc.name)}</span><pre style="margin:2px 0 0 0; white-space:pre-wrap; font-size:11px;">${escapeHtml(inputStr)}</pre></div>`;
    }).join('');
    html += `<div style="margin-bottom:8px;"><div style="color:var(--yorha-fg-dim); font-size:11px; margin-bottom:2px;">[工具调用]</div>${tcHtml}</div>`;
  }
  if (r.content) {
    html += `<div><div style="color:var(--yorha-fg-dim); font-size:11px; margin-bottom:2px;">[回复]</div><div style="white-space:pre-wrap;">${escapeHtml(r.content)}</div></div>`;
  }
  return html || '(无内容)';
}

function escapeHtml(s) {
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

let statusUpdateTimer = null;
function scheduleStatusUpdate() {
  if (statusUpdateTimer) return;
  statusUpdateTimer = setTimeout(() => {
    statusUpdateTimer = null;
    loadStatus();
  }, 1000); // 1秒防抖更新系统指标，避免并发高频打爆网络
}

// ── WebSocket 实时同步 ──
function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(proto + '//' + location.host + '/ws');

  ws.onmessage = (e) => {
    try {
      const entry = JSON.parse(e.data);
      const query = $('searchInput').value.toLowerCase();
      const body = $('logBody');
      const idx = logs.findIndex(l => l.id === entry.id);

      if (idx >= 0) {
        logs[idx] = entry; // 内存同步
        // 单行直连更新（Fast Path）：无损直连，完全不触发全表拔插与重绘
        const tr = body.querySelector(`tr[data-id="${entry.id}"]`);
        if (tr) {
          updateSingleRow(tr, entry);
        } else if (matchesLogQuery(entry, query)) {
          renderLogs();
        }
      } else {
        // 全新请求插入头部
        logs.unshift(entry);
        if (logs.length > 200) logs.length = 200;

        if (matchesLogQuery(entry, query)) {
          $('logTable').style.display = '';
          $('emptyState').style.display = 'none';
          const newTr = buildRowElement(entry);
          body.insertBefore(newTr, body.firstChild);
          while (body.children.length > 200) {
            body.lastElementChild.remove();
          }
        }
      }

      // 如果抽屉正打开着当前记录，仅轻量同步抽屉头部的延迟和状态，不重复发送大网络请求
      if (entry.id === currentDetailId) {
        updateDrawerHeaderInfo(entry);
      }

      scheduleStatusUpdate();
    } catch(ex) {}
  };

  ws.onclose = () => setTimeout(connectWS, 2000);
  ws.onerror = () => ws.close();
}

// ── 清空日志 ──
async function clearLogs() {
  try {
    await fetch('/api/logs', { method: 'DELETE' });
  } catch(e) {}
  logs = [];
  closeDrawer();
  renderLogs();
  loadStatus();
}

// ── 加载设置 ──
async function loadSettings() {
  try {
    const r = await fetch('/api/config');
    const cfg = await r.json();
    $('cfgPort').value = cfg.port || 8188;
    $('cfgLanAccess').checked = cfg.lan_access || false;
    renderProviders(cfg.providers || [], cfg.active_provider || '');
    $('cfgVerbose').checked = cfg.verbose_logging || false;
    $('cfgThinkingMode').value = cfg.thinking_mode || '';
    $('cfgReasoningEffort').value = cfg.reasoning_effort || 'high';
    $('cfgPlacement').value = cfg.system_prompt_placement || 'first';
    $('cfgExtraPlacement').value = cfg.extra_prompt_placement || 'none';
    $('cfgExtraPrompt').value = cfg.extra_prompt || '';
    $('extraPromptGroup').style.display = (cfg.extra_prompt_placement && cfg.extra_prompt_placement !== 'none') ? '' : 'none';
    $('effortGroup').style.display = cfg.thinking_mode === 'enabled' ? '' : 'none';
    $('cfgMaxTokensMode').value = cfg.max_tokens_mode || '';
    $('cfgMaxTokensCustom').value = cfg.max_tokens_custom || 8000;
    $('maxTokensCustomGroup').style.display = cfg.max_tokens_mode === 'custom' ? '' : 'none';
    $('cfgAntiLoop').checked = cfg.anti_loop_enabled || false;
    $('antiloopSubSettings').style.display = cfg.anti_loop_enabled ? '' : 'none';
    $('cfgAntiloopRetryModel').value = cfg.antiloop_retry_model || 'deepseek-v4-flash';
    $('cfgAntiloopRetryThinking').value = cfg.antiloop_retry_thinking || '';
    $('cfgAntiloopRetryEffort').value = cfg.antiloop_retry_effort || 'high';
    $('antiloopEffortGroup').style.display = cfg.antiloop_retry_thinking === 'enabled' ? '' : 'none';
    $('cfgAntiloopCheckTokens').value = cfg.antiloop_check_tokens || 0;
    $('cfgAntiHallucination').checked = cfg.anti_hallucination_enabled || false;
    $('cfgAntiHallucinationPrompt').value = cfg.anti_hallucination_prompt || '';
    $('antihallucinationSubSettings').style.display = cfg.anti_hallucination_enabled ? '' : 'none';



    $('cfgDebugMode').checked = cfg.debug_mode || false;
    $('cfgAutoReasoning').checked = cfg.auto_reasoning_content !== false;
    $('cfgAutoFixEmpty').checked = cfg.auto_fix_empty_content || false;

    $('cfgAnalysisEnabled').checked = cfg.analysis_enabled !== false;
    $('analysisSubSettings').style.display = cfg.analysis_enabled !== false ? '' : 'none';
    $('cfgAnalysisRetentionDays').value = cfg.analysis_retention_days || 7;

    $('cfgTheme').value = cfg.theme || 'yorha';
    if ($('cfgLanguage')) {
      $('cfgLanguage').value = cfg.language || 'zh';
    }

    setRestartBanner(cfg.restart_required, cfg.restart_reasons);

    // 回填完成后，重新刷新并转换自定义的尼尔下拉组件
    convertSelectsToYorha();

    if (typeof applyI18n === 'function') applyI18n();
  } catch(e) {
    triggerAlert(t('alert.load_failed') || '加载配置失败');
  }
}

// ── 供应商（多上游）管理 ──
// 语义：数组顺序即优先级，第一位是默认供应商（路由未命中时的兜底目标）；启用开关决定是否参与路由。
let providersData = [];
let activeProviderIndex = 0; // 保留变量：恒为 0（第一位即默认供应商）
let persistProvidersTimer = null;

// 路由表预览缓存：由后端 /api/route/preview 提供，前端不重复计算冲突
let routePreview = { models: [], byPublicId: {}, byProviderModel: {}, conflicted: 0, unroutable: 0, defaultProvider: '' };

const PROVIDER_ID_PATTERN = /^[A-Za-z0-9_-]+$/;

function isValidProviderId(id) {
  return PROVIDER_ID_PATTERN.test((id || '').trim());
}

// 返回与给定 ID 冲突的其它供应商下标（不区分大小写）；excludeIdx 为需要排除的下标
function findProviderIdConflict(id, excludeIdx) {
  const key = (id || '').trim().toLowerCase();
  if (!key) return -1;
  return providersData.findIndex((p, i) => i !== excludeIdx && (p.ID || '').trim().toLowerCase() === key);
}

// 由名称推导一个合法且未被占用的供应商 ID：英文名称取同名，否则 providerN；冲突自动追加序号
function generateProviderId(name) {
  const trimmed = (name || '').trim();
  let base = isValidProviderId(trimmed) ? trimmed : '';
  if (!base) {
    let n = 1;
    do {
      base = 'provider' + n;
      n++;
    } while (findProviderIdConflict(base, -1) >= 0);
  }
  let id = base;
  let seq = 2;
  while (findProviderIdConflict(id, -1) >= 0) {
    id = base + seq;
    seq++;
  }
  return id;
}

async function refreshRoutePreview() {
  try {
    const r = await fetch('/api/route/preview');
    const data = await r.json();
    const byPublicId = {};
    const byProviderModel = {};
    (data.models || []).forEach(m => {
      byPublicId[m.public_id] = m;
      byProviderModel[(m.provider_id || '') + '||' + (m.upstream_id || '')] = m;
    });
    routePreview = {
      models: data.models || [],
      byPublicId,
      byProviderModel,
      conflicted: data.conflicted || 0,
      unroutable: data.unroutable || 0,
      defaultProvider: data.default_provider || ''
    };
  } catch (e) {
    routePreview = { models: [], byPublicId: {}, byProviderModel: {}, conflicted: 0, unroutable: 0, defaultProvider: '' };
  }
}

function renderProviders(providers, active) {
  providersData = (providers || []).map(p => ({
    ID: p.id || '',
    Name: p.name || '',
    Enabled: p.enabled !== false,
    BaseURL: p.base_url || '',
    AnthropicBaseURL: p.anthropic_base_url || '',
    APIKey: p.api_key || '',
    Models: Array.isArray(p.models) ? p.models.map(m => ({
      id: m.id || '',
      name: m.name || m.id || '',
      group: m.group || '',
      protocol: m.protocol || 'openai'
    })) : []
  }));
  if (providersData.length === 0) {
    providersData = [{ ID: 'DeepSeek', Name: 'DeepSeek', Enabled: true, BaseURL: 'https://api.deepseek.com', AnthropicBaseURL: '', APIKey: '', Models: [] }];
  }
  providersData.forEach(p => { if (!p.ID) p.ID = generateProviderId(p.Name); });
  activeProviderIndex = 0; // 默认供应商 = 列表第一位
  fillProviderEditor();
  updateDefaultProviderLabel();

  if (typeof renderModelProvidersList === 'function') {
    refreshRoutePreview().then(() => {
      renderModelProvidersList();
      renderCurrentProviderDetail();
    });
  }
}

function updateDefaultProviderLabel() {
  const el = $('cfgDefaultProviderName');
  if (!el) return;
  const first = providersData[0];
  el.textContent = first ? `${first.Name || '(未命名)'} / ${first.ID || ''}` : '—';
}

function providerPayload(p, idx) {
  return {
    id: p.ID,
    name: p.Name,
    enabled: idx === 0 ? true : p.Enabled !== false, // 第一位（默认供应商）不可关闭
    base_url: p.BaseURL,
    anthropic_base_url: p.AnthropicBaseURL,
    api_key: p.APIKey,
    models: p.Models || []
  };
}

async function persistProvidersConfig(silent = false) {
  const payload = {
    providers: providersData.map((p, i) => providerPayload(p, i))
  };

  try {
    const r = await fetch('/api/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    const resp = await r.json();
    setRestartBanner(resp.restart_required, resp.restart_reasons);
    if (resp.warnings && resp.warnings.length) {
      triggerAlert(resp.warnings.join('；'));
    } else if (!silent) {
      triggerAlert(t('alert.saved') || '配置已保存');
    }
    await refreshRoutePreview();
  } catch (e) {
    if (!silent) {
      triggerAlert(t('alert.save_failed') || '保存配置失败');
    }
  }
}

function debouncePersistProviders(delayMs = 600) {
  if (persistProvidersTimer) clearTimeout(persistProvidersTimer);
  persistProvidersTimer = setTimeout(() => {
    persistProvidersConfig(true);
  }, delayMs);
}

// 设置页供应商编辑区：始终对应默认供应商（列表第一位），名称/地址/密钥在此直接编辑
function fillProviderEditor() {
  const p = providersData[0];
  if (!p) return;
  $('cfgProviderName').value = p.Name || '';
  $('cfgProviderBase').value = p.BaseURL || '';
  $('cfgProviderAnthropicBase').value = p.AnthropicBaseURL || '';
  $('cfgProviderKey').value = p.APIKey || '';
}

function syncProviderFromEditor() {
  const p = providersData[0];
  if (!p) return;
  p.Name = $('cfgProviderName').value;
  p.BaseURL = $('cfgProviderBase').value;
  p.AnthropicBaseURL = $('cfgProviderAnthropicBase').value;
  p.APIKey = $('cfgProviderKey').value;
  updateDefaultProviderLabel();
  if (typeof renderModelProvidersList === 'function') renderModelProvidersList();
}

function toggleProviderKeyVisibility() {
  const input = $('cfgProviderKey');
  const btn = input.parentElement.querySelector('.yorha-eye-btn');
  if (!input || !btn) return;
  if (input.type === 'password') {
    input.type = 'text';
    btn.classList.add('visible');
  } else {
    input.type = 'password';
    btn.classList.remove('visible');
  }
}

// ── 保存配置 ──
async function saveSettings() {
  const payload = {
    port: parseInt($('cfgPort').value) || 8188,
    lan_access: $('cfgLanAccess').checked,
    providers: providersData.map((p, i) => providerPayload(p, i)),
    verbose_logging: $('cfgVerbose').checked,
    thinking_mode: $('cfgThinkingMode').value,
    reasoning_effort: $('cfgReasoningEffort').value,
    system_prompt_placement: $('cfgPlacement').value,
    extra_prompt_placement: $('cfgExtraPlacement').value,
    extra_prompt: $('cfgExtraPrompt').value,
    max_tokens_mode: $('cfgMaxTokensMode').value,
    max_tokens_custom: parseInt($('cfgMaxTokensCustom').value) || 0,
    anti_loop_enabled: $('cfgAntiLoop').checked,
    antiloop_retry_model: $('cfgAntiloopRetryModel').value,
    antiloop_retry_thinking: $('cfgAntiloopRetryThinking').value,
    antiloop_retry_effort: $('cfgAntiloopRetryEffort').value,
    antiloop_check_tokens: parseInt($('cfgAntiloopCheckTokens').value) || 0,
    anti_hallucination_enabled: $('cfgAntiHallucination').checked,
    anti_hallucination_prompt: $('cfgAntiHallucinationPrompt').value,

    debug_mode: $('cfgDebugMode').checked,
    auto_reasoning_content: $('cfgAutoReasoning').checked,
    auto_fix_empty_content: $('cfgAutoFixEmpty').checked,
    analysis_enabled: $('cfgAnalysisEnabled').checked,
    analysis_persistence: $('cfgAnalysisEnabled').checked,
    analysis_persist_raw_bodies: $('cfgAnalysisEnabled').checked,
    analysis_retention_days: parseInt($('cfgAnalysisRetentionDays').value) || 7,
    theme: $('cfgTheme').value,
    language: $('cfgLanguage') ? $('cfgLanguage').value : 'zh',
  };

  try {
    const r = await fetch('/api/config', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload)
    });
    const resp = await r.json();
    setRestartBanner(resp.restart_required, resp.restart_reasons);

    // 主题写入 localStorage
    const selectedTheme = $('cfgTheme').value;
    localStorage.setItem('dsplus_theme', selectedTheme);
    document.body.className = 'theme-' + selectedTheme;

    // 语言切换：如果改变了则立即应用
    const newLang = payload.language;
    if (newLang && typeof setLanguage === 'function' && newLang !== (typeof getCurrentLang === 'function' ? getCurrentLang() : '')) {
      setLanguage(newLang);
    }

    if (resp.status === 'saved') {
      const msg = resp.restart_required ? t('alert.saved_restart') || '配置已保存，重启后生效' : t('alert.saved') || '配置已保存';
      triggerAlert(msg);
    } else {
      const msg = resp.restart_required ? t('alert.saved_restart') || '配置已保存，等待重启' : t('alert.saved') || '配置已保存';
      triggerAlert(msg);
    }
  } catch(e) {
    triggerAlert(t('alert.save_failed') || '保存失败');
  }
}

// ── 清除分析历史会话 ──
async function clearAnalysisHistory() {
  if (!confirm(t('alert.analysis_clear_confirm') || '确定要清空所有分析历史会话和本地日志文件吗？此操作不可恢复。')) {
    return;
  }
  try {
    const r = await fetch('/api/analysis/sessions', { method: 'DELETE' });
    const resp = await r.json();
    if (resp.status === 'cleared') {
      triggerAlert(t('alert.analysis_cleared') || '分析历史已全部清除');
      currentSessionId = null;
      loadAnalysisSessions();
    } else {
      triggerAlert(t('alert.analysis_clear_failed') || '清除失败');
    }
  } catch(e) {
    triggerAlert('请求清除失败');
  }
}

// ── 恢复默认 ──
function resetSettings() {
  renderProviders([{ name: 'DeepSeek', base_url: 'https://api.deepseek.com', anthropic_base_url: '', api_key: '' }], 'DeepSeek');
  $('cfgPort').value = 8188;
  $('cfgLanAccess').checked = false;
  $('cfgVerbose').checked = false;
  $('cfgThinkingMode').value = '';
  $('cfgReasoningEffort').value = 'high';
  $('cfgPlacement').value = 'first';
  $('cfgExtraPlacement').value = 'none';
  $('cfgExtraPrompt').value = '';
  $('effortGroup').style.display = 'none';
  $('extraPromptGroup').style.display = 'none';
  $('cfgMaxTokensMode').value = '';
  $('cfgMaxTokensCustom').value = 8000;
  $('maxTokensCustomGroup').style.display = 'none';
  $('cfgAntiLoop').checked = false;
  $('antiloopSubSettings').style.display = 'none';
  $('cfgAntiloopRetryModel').value = 'deepseek-v4-flash';
  $('cfgAntiloopRetryThinking').value = '';
  $('cfgAntiloopRetryEffort').value = 'high';
  $('antiloopEffortGroup').style.display = 'none';
  $('cfgAntiloopCheckTokens').value = 0;
  $('cfgDebugMode').checked = false;
  $('cfgAutoReasoning').checked = true;
  $('cfgAutoFixEmpty').checked = false;

  $('cfgAnalysisEnabled').checked = true;
  $('analysisSubSettings').style.display = 'none';
  $('cfgAnalysisRetentionDays').value = 7;

  // 语言重置为当前检测默认（简单用 zh，真正检测在后端）
  if ($('cfgLanguage')) $('cfgLanguage').value = 'zh';

  // 主题重置
  localStorage.setItem('dsplus_theme', 'yorha');
  applyTheme();

  // 刷新下拉组件显示
  convertSelectsToYorha();

  // 触发 change 联动
  const selects = document.querySelectorAll('#settings select, #settings input[type=checkbox]');
  selects.forEach(el => el.dispatchEvent(new Event('change')));

  if (typeof applyI18n === 'function') applyI18n();

  triggerAlert(t('alert.reset_done') || '已重置为默认值');
}

function setRestartBanner(visible, reasons) {
  const banner = $('restartBanner');
  if (!banner) return;
  banner.style.display = visible ? '' : 'none';
  if (visible) {
    const names = Array.isArray(reasons) && reasons.length ? reasons.join('、') : '';
    const base = t('actions.restart_reason') || '设置已保存，重启服务后生效。';
    $('restartReasonText').textContent = names ? (names + (t('actions.restart_suffix') || '已保存，重启服务后生效。')) : base;
  }
  const btn = $('btnRestartService');
  if (btn) {
    btn.disabled = false;
    btn.textContent = t('actions.restart') || '重启服务';
  }
}

async function restartService() {
  const btn = $('btnRestartService');
  const nextPort = parseInt($('cfgPort').value) || 8188;
  if (btn) {
    btn.disabled = true;
    btn.textContent = t('alert.restarting') || '正在重启...';
  }
  try {
    await fetch('/api/restart', { method: 'POST' });
    triggerAlert(t('alert.restarting') || '正在重启服务...');
    setTimeout(() => {
      location.href = 'http://127.0.0.1:' + nextPort + '/';
    }, 1800);
  } catch(e) {
    triggerAlert(t('alert.restart_manual') || '重启请求失败，请手动重启 DSPlus');
    if (btn) {
      btn.disabled = false;
      btn.textContent = t('actions.restart') || '重启服务';
    }
  }
}

// ── 寄叶系统警报 Banner (替代 Toast) ──
function triggerAlert(msg) {
  const banner = $('yorhaAlert');
  if (!banner) return;
  $('alertMessage').textContent = msg;
  banner.classList.add('show');
  setTimeout(() => {
    banner.classList.remove('show');
  }, 2500);
}

// ── 加载分析历史会话 ──
async function loadAnalysisSessions() {
  if (analysisSessionsLoading) return;
  analysisSessionsLoading = true;
  analysisSessionsOffset = 0;
  analysisSessionsHasMore = true;

  const container = $('sessionListContainer');
  container.innerHTML = '<div class="empty-state">' + (t('common.loading') || '读取中...') + '</div>';
  try {
    const r = await fetch('/api/analysis/sessions?limit=50&offset=0');
    sessionSummaries = await r.json();
    analysisSessionsLoading = false;
    
    if (!sessionSummaries || sessionSummaries.length < 50) {
      analysisSessionsHasMore = false;
    }
    renderSessionList();
  } catch(e) {
    analysisSessionsLoading = false;
    container.innerHTML = '<div class="empty-state"><p>' + (t('analysis.load_failed') || '加载历史失败') + '</p></div>';
  }
}

async function loadNextAnalysisSessions() {
  if (analysisSessionsLoading || !analysisSessionsHasMore) return;
  analysisSessionsLoading = true;
  
  analysisSessionsOffset += 50;
  
  const container = $('sessionListContainer');
  const loader = document.createElement('div');
  loader.className = 'session-loading-indicator';
  loader.style.cssText = 'text-align: center; padding: 10px; font-size: 11px; color: var(--yorha-fg-dim);';
  loader.textContent = t('common.loading') || '读取中...';
  container.appendChild(loader);
  
  try {
    const r = await fetch(`/api/analysis/sessions?limit=50&offset=${analysisSessionsOffset}`);
    const nextList = await r.json();
    
    loader.remove();
    analysisSessionsLoading = false;
    
    if (!nextList || nextList.length < 50) {
      analysisSessionsHasMore = false;
    }
    
    if (nextList && nextList.length > 0) {
      sessionSummaries = sessionSummaries.concat(nextList);
      appendSessionList(nextList);
    }
  } catch(e) {
    loader.remove();
    analysisSessionsLoading = false;
  }
}

function createSessionDOM(s) {
  const time = new Date(s.start_time).toLocaleTimeString('zh-CN', {hour:'2-digit',minute:'2-digit',second:'2-digit'});
  const fmtType = (s.format || 'openai').toUpperCase();
  const model = s.models || '--';
  const status = s.status || 200;
  const statusClass = status >= 400 ? 'status-400' : 'status-200';
  const badgeClass = s.format === 'anthropic' ? 'badge-anthropic' : 'badge-openai';

  const item = document.createElement('div');
  item.className = 'session-item' + (s.id === currentSessionId ? ' active' : '');
  
  item.innerHTML = `
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px;">
      <span style="font-size: 11px; color: var(--yorha-fg-dim); font-family: monospace;">${time}</span>
      <span class="badge ${badgeClass}" style="padding: 1px 6px; font-size: 10px;">${fmtType}</span>
    </div>
    <div style="display: flex; justify-content: space-between; align-items: center; gap: 8px; min-width: 0;">
      <span style="font-size: 12px; font-weight: 500; color: inherit; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; min-width: 0;" title="${model}">${model}</span>
      <span class="${statusClass}" style="font-size: 11px; font-weight: 600; font-family: monospace; flex-shrink: 0;">${status}</span>
    </div>
  `;
  item.addEventListener('click', () => selectSession(s.id));
  return item;
}

function renderSessionList() {
  const container = $('sessionListContainer');
  container.innerHTML = '';
  if (sessionSummaries.length === 0) {
    container.innerHTML = '<div class="empty-state"><p>' + (t('analysis.empty') || '暂无请求分析记录') + '</p><p style="font-size:11px;margin-top:4px">' + (t('analysis.select_prompt') || '发送 API 请求即可生成诊断') + '</p></div>';
    $('analysisMainPanel').style.display = 'none';
    $('analysisMainEmpty').style.display = '';
    return;
  }

  sessionSummaries.forEach(s => {
    container.appendChild(createSessionDOM(s));
  });
}

function appendSessionList(nextList) {
  const container = $('sessionListContainer');
  nextList.forEach(s => {
    container.appendChild(createSessionDOM(s));
  });
}

function selectSession(id) {
  currentSessionId = id;
  renderSessionList();
  showSessionDetail(id);
}

// ── 展示会话诊断详情 ──
async function showSessionDetail(id) {
  $('analysisMainPanel').style.display = '';
  $('analysisMainEmpty').style.display = 'none';

  try {
    const r = await fetch('/api/analysis/sessions/' + id);
    const s = await r.json();

    $('ansSessionId').textContent = 'REPORT #' + id.substring(5, 11).toUpperCase();
    $('ansRequestCount').textContent = s.request_count;
    const tokenIn = t('analysis.token_in') || '入';
    const tokenOut = t('analysis.token_out') || '出';
    const tokenHit = t('analysis.token_hit') || '命中';
    $('ansTokens').textContent = `${s.total_tokens} (${tokenIn}: ${s.prompt_tokens} / ${tokenOut}: ${s.completion_tokens})`;
    
    const ratio = formatCacheRatio(s.cache_hit_tokens, s.cache_miss_tokens);
    $('ansCacheRatio').textContent = `${ratio} (${tokenHit}: ${s.cache_hit_tokens})`;
    
    const retriesLabel = t('analysis.retries', { count: s.retries }) || (s.retries + ' 次重试');
    $('ansRetries').textContent = retriesLabel;
    $('ansRetries').style.display = s.retries > 0 ? '' : 'none';
    const errorsLabel = t('analysis.errors', { count: s.errors }) || (s.errors + ' 异常');
    $('ansErrors').textContent = errorsLabel;
    $('ansErrors').style.display = s.errors > 0 ? '' : 'none';

    // 提取 tools 信息
    let tools = [];
    for (let tid in s.turns) {
      const turn = s.turns[tid];
      const primaryEvent = turn.events.find(e => e.phase === 'primary');
      if (primaryEvent && primaryEvent.request && primaryEvent.request.tools && primaryEvent.request.tools.length > 0) {
        tools = primaryEvent.request.tools;
        break;
      }
    }
    
    const toolsWrapper = $('ansToolsWrapper');
    const toolsContent = $('ansToolsContent');
    const toolsArrow = $('toolsArrow');
    if (tools && tools.length > 0) {
      toolsWrapper.style.display = '';
      toolsContent.innerHTML = tools.map(t => `<div style="padding: 2px 0;">• ${escapeHtml(JSON.stringify(t))}</div>`).join('');
      toolsContent.style.display = 'none';
      toolsArrow.className = 'arrow collapsed';
      toolsArrow.textContent = '▼';
    } else {
      toolsWrapper.style.display = 'none';
    }

    await loadChatTimeline(id, true);
  } catch(e) {
    triggerAlert(t('analysis.load_detail_failed') || '加载详情失败');
  }
}

function toggleToolsFold() {
  const el = $('ansToolsContent');
  const arrow = $('toolsArrow');
  if (el.style.display === 'none') {
    el.style.display = 'block';
    arrow.className = 'arrow';
    arrow.textContent = '▲';
  } else {
    el.style.display = 'none';
    arrow.className = 'arrow collapsed';
    arrow.textContent = '▼';
  }
}

async function loadChatTimeline(sessionId, reset = false) {
  const timeline = $('chatTimeline');
  if (reset) {
    timeline.innerHTML = '';
    analysisTimelineState = {
      sessionId,
      offset: 0,
      limit: 100,
      total: 0,
      loading: false,
      hasMore: true
    };
  }
  if (analysisTimelineState.loading || !analysisTimelineState.hasMore) return;

  analysisTimelineState.loading = true;
  const loadingNode = document.createElement('div');
  loadingNode.className = 'system-notice';
  loadingNode.textContent = t('analysis.loading') || t('common.loading') || '加载中...';
  timeline.appendChild(loadingNode);

  try {
    const url = `/api/analysis/sessions/${sessionId}/timeline?offset=${analysisTimelineState.offset}&limit=${analysisTimelineState.limit}`;
    const r = await fetch(url);
    if (!r.ok) throw new Error('timeline request failed');
    const page = await r.json();
    loadingNode.remove();

    analysisTimelineState.total = page.total || 0;
    const items = page.items || [];
    items.forEach(item => timeline.appendChild(createTimelineItemDOM(sessionId, item)));
    analysisTimelineState.offset += items.length;
    analysisTimelineState.hasMore = analysisTimelineState.offset < analysisTimelineState.total;

    if (analysisTimelineState.hasMore) {
      const more = document.createElement('button');
      more.className = 'btn btn-secondary';
      more.style.margin = '12px auto';
      more.style.display = 'block';
      more.textContent = t('analysis.load_more') || '加载更多';
      more.onclick = () => {
        more.remove();
        loadChatTimeline(sessionId, false);
      };
      timeline.appendChild(more);
    }
  } catch (e) {
    loadingNode.textContent = t('analysis.timeline_load_failed') || '时间线加载失败';
  } finally {
    analysisTimelineState.loading = false;
  }
}

// createUserTurnDOM 渲染一个 UserTurnGroup 为可折叠区块，内部按 step 顺序渲染。
function createUserTurnDOM(sessionId, group) {
  const container = document.createElement('div');
  container.className = 'user-turn-group';
  container.style.cssText = 'margin-bottom: 16px; border: 1px solid var(--yorha-border); border-radius: 4px; overflow: hidden;';

  // 头部摘要
  const durStr = group.summary.duration_ms >= 1000
    ? (group.summary.duration_ms / 1000).toFixed(1) + 's'
    : group.summary.duration_ms + 'ms';
  const headerParts = [];
  if (group.summary.tool_count > 0) headerParts.push(`${group.summary.tool_count}个工具` + (group.summary.tool_names ? `(${group.summary.tool_names})` : ''));
  const nThinking = group.summary.thinking_chars || 0;
  if (nThinking > 0) headerParts.push(t('analysis.thinking_chars', { n: nThinking }) || `思考${nThinking}字`);
  const nReply = group.summary.reply_chars || 0;
  if (nReply > 0) headerParts.push(t('analysis.reply_chars', { n: nReply }) || `回复${nReply}字`);
  const durLabel = t('analysis.duration', { dur: durStr }) || `耗时${durStr}`;
  headerParts.push(durLabel);
  const headerMeta = headerParts.join(' · ');

  const timeStr = new Date(group.start_time).toLocaleTimeString('zh-CN', {hour:'2-digit',minute:'2-digit',second:'2-digit'});
  const userQ = t('analysis.user_question', { id: group.turn_id }) || `用户提问 #${group.turn_id}`;
  const header = document.createElement('div');
  header.style.cssText = 'padding: 8px 12px; background: rgba(0,0,0,0.06); cursor: pointer; display:flex; justify-content:space-between; align-items:center; font-size: 11px;';
  header.innerHTML = `
    <span style="color: var(--yorha-fg-dim);">▼ ${userQ} (${timeStr})</span>
    <span style="color: var(--yorha-accent); font-size: 10px;">${escapeHtml(headerMeta)}</span>
  `;

  const body = document.createElement('div');
  body.style.cssText = 'padding: 10px 12px;';

  // 渲染 steps
  (group.steps || []).forEach(step => {
    body.appendChild(createStepDOM(sessionId, step));
  });

  // 渲染 events（analyzer/retry）
  (group.events || []).forEach(ev => {
    if (ev.phase === 'analyzer' || ev.phase === 'retry') {
      const notice = document.createElement('div');
      notice.className = ev.phase === 'analyzer' ? 'system-notice danger' : 'system-notice success';
      const title = ev.phase === 'analyzer' ? (t('analysis.analyzer_triggered') || '系统：并行防无限思考分析已触发') : (t('analysis.retry_executed') || '系统：反无限思考重试已执行');
      notice.innerHTML = `
        <div class="system-notice-title">
          <span>${title}</span>
          ${ev.log_id ? `<span class="jump-raw-btn" onclick="jumpToRawLog(${ev.log_id})">[原始数据]</span>` : ''}
        </div>
        <div>${escapeHtml(ev.response ? (ev.response.analyzer_judgment || '') : '')}</div>
      `;
      body.appendChild(notice);
    }
  });

  header.addEventListener('click', () => {
    const isVisible = body.style.display !== 'none';
    body.style.display = isVisible ? 'none' : '';
    header.querySelector('span').textContent = (isVisible ? '▶' : '▼') + header.querySelector('span').textContent.slice(1);
  });

  container.appendChild(header);
  container.appendChild(body);
  return container;
}

// createStepDOM 渲染单个 InteractionStep
function createStepDOM(sessionId, step) {
  const node = document.createElement('div');
  node.style.cssText = 'margin-bottom: 8px;';

  switch (step.kind) {
    case 'user_message': {
      node.className = 'yorha-msg-bubble user';
      const roleUser = t('analysis.role_user') || '用户';
      const emptyC = t('analysis.empty_content') || '（空内容）';
      node.innerHTML = `
        <div class="bubble-meta"><span>${roleUser}</span></div>
        ${genRefCollapsibleHtml(step.preview || emptyC, step.content_ref, sessionId, 'content')}
      `;
      break;
    }
    case 'assistant_thinking': {
      const rLen = step.reasoning_preview ? step.reasoning_preview.length : 0;
      node.innerHTML = `
        <div class="log-chip" data-session-id="${escapeHtml(sessionId)}" data-ref="${escapeHtml(encodeContentRef(step.reasoning_ref))}" data-preview="${escapeHtml(step.reasoning_preview || '')}">
          <div class="log-chip-title" onclick="toggleRefChip(this)">🤔 ${t('analysis.thinking_path', { n: rLen }) || ('DEEPSEEK THINKING PATH (点击展开/折叠 ' + rLen + '字)')}${step.duration_ms ? ' (' + (step.duration_ms >= 1000 ? (step.duration_ms/1000).toFixed(1)+'s' : step.duration_ms+'ms') + ')' : ''}</div>
          <div class="log-chip-content" style="white-space:pre-wrap;"></div>
        </div>
      `;
      break;
    }
    case 'tool_call': {
      const tc = { name: step.tool_name, input: step.tool_input, id: step.tool_use_id };
      const ref = step.tool_input_ref || null;
      node.innerHTML = `
        <div style="padding: 6px 8px; border-left: 3px solid var(--yorha-accent); background: rgba(0,0,0,0.04); border-radius: 0 3px 3px 0;">
          <div style="font-weight:600; font-size:11px; color: var(--yorha-accent); margin-bottom: 4px;">🔧 ${t('analysis.role_tool_call') || '工具调用'}</div>
          ${renderToolCallCard(tc, ref, sessionId)}
        </div>
      `;
      break;
    }
    case 'tool_result': {
      const lenStr = step.preview ? step.preview.length + (t('analysis.thinking_chars', {n: ''}).includes('字') ? '字' : '') : '';
      const pairedHint = step.paired_tool_call_id ? ' [已配对]' : '';
      node.className = 'system-notice success';
      node.style.cssText = 'border-color: var(--yorha-accent); background: rgba(205,90,63,0.03); margin-bottom: 8px;';
      const roleToolRes = t('analysis.role_tool_result') || '工具结果 (Tool Result)';
      const emptyC2 = t('analysis.empty_content') || '（空内容）';
      node.innerHTML = `
        <div class="system-notice-title" style="color:var(--yorha-accent)">
          <span>📎 ${roleToolRes}${lenStr ? ' ' + lenStr : ''}${pairedHint}</span>
        </div>
        ${genRefCollapsibleHtml(step.preview || emptyC2, step.content_ref, sessionId, 'tool_result')}
      `;
      break;
    }
    case 'assistant_reply': {
      node.className = 'yorha-msg-bubble assistant';
      const durHint = step.duration_ms ? `<span style="color:var(--yorha-fg-dim); font-size:10px;">${step.duration_ms >= 1000 ? (step.duration_ms/1000).toFixed(1)+'s' : step.duration_ms+'ms'}</span>` : '';
      const roleAi = t('analysis.role_ai') || 'AI 响应';
      const emptyC3 = t('analysis.empty_content') || '（空内容）';
      node.innerHTML = `
        <div class="bubble-meta"><span>🤖 ${roleAi}</span> ${durHint}</div>
        ${genRefCollapsibleHtml(step.preview || emptyC3, step.content_ref, sessionId, 'content')}
      `;
      break;
    }
    default:
      return document.createComment('unknown step kind: ' + step.kind);
  }
  return node;
}

function createTimelineItemDOM(sessionId, item) {
  if (item.type === 'event') {
    const notice = document.createElement('div');
    notice.className = item.phase === 'analyzer' ? 'system-notice danger' : 'system-notice success';
    const title = item.phase === 'analyzer' ? (t('analysis.analyzer_triggered') || '系统：并行防无限思考分析已触发') : (t('analysis.retry_executed') || '系统：反无限思考重试已执行');
    notice.innerHTML = `
      <div class="system-notice-title">
        <span>${title}</span>${item.turn_id ? `<span class="request-seq-badge">#${item.turn_id}</span>` : ''}
        ${item.log_id ? `<span class="jump-raw-btn" onclick="jumpToRawLog(${item.log_id})">[原始数据]</span>` : ''}
      </div>
      <div>${escapeHtml(item.preview || '')}</div>
    `;
    return notice;
  }

  const role = item.role || 'user';
  const isAssistant = role === 'assistant';
  const isTool = role === 'tool';

  let isPureToolCall = false;
  if (isAssistant && item.tool_calls && item.tool_calls.length > 0) {
    const joinedTools = item.tool_calls.join('\n');
    if (!item.preview || item.preview === joinedTools) {
      isPureToolCall = true;
    }
  }

  const node = document.createElement('div');
  const useNoticeStyle = isTool || isPureToolCall;
  node.className = useNoticeStyle ? 'system-notice success' : `yorha-msg-bubble ${isAssistant ? 'assistant' : 'user'}`;
  if (useNoticeStyle) {
    node.style.borderColor = 'var(--yorha-accent)';
    node.style.background = 'rgba(205,90,63,0.03)';
  }

  let roleName = t('analysis.role_user') || '用户';
  if (role === 'system') {
    roleName = t('analysis.role_system') || '系统 (System)';
  } else if (isTool) {
    roleName = t('analysis.role_tool_result') || '工具返回内容 (Tool Result)';
  } else if (isPureToolCall) {
    roleName = t('analysis.role_tool_call') || '调用工具内容 (Tool Call)';
  } else if (isAssistant) {
    roleName = t('analysis.role_ai') || 'AI 响应';
  }

  const contentField = isTool ? 'tool_result' : 'content';
  let bodyHtml = '';

  if (isAssistant) {
    let reasoningHtml = '';
    if (item.reasoning_content) {
      reasoningHtml = `
        <div class="log-chip">
          <div class="log-chip-title" onclick="const content = this.nextElementSibling; content.style.display = content.style.display === 'none' ? 'block' : 'none';">🤔 DEEPSEEK THINKING PATH (点击展开/折叠)</div>
          <div class="log-chip-content" style="display:none; white-space:pre-wrap;">${escapeHtml(item.reasoning_content)}</div>
        </div>
      `;
    }

    if (isPureToolCall) {
      const toolHtml = item.tool_calls.map(tc => renderLocalToolCallCard(tc)).join('');
      bodyHtml = reasoningHtml + `
        <div style="margin-top:6px;">
          ${toolHtml}
        </div>
      `;
    } else {
      let textHtml = '';
      if (!item.preview) {
        textHtml = `<div style="color:var(--yorha-fg-dim); font-style:italic;">${t('analysis.no_text_output') || '仅包含思考或工具调用，无文本输出'}</div>`;
      } else {
        textHtml = genLocalCollapsibleHtml(item.preview, contentField);
      }

      let toolHtml = '';
      if (item.tool_calls && item.tool_calls.length > 0) {
        const cards = item.tool_calls.map(tc => renderLocalToolCallCard(tc)).join('');
        toolHtml = `
          <div style="margin-bottom:10px; padding: 10px; border-left: 3px solid var(--yorha-accent); background: rgba(0,0,0,0.05);">
            <div style="font-weight:600; font-size:11px; color: var(--yorha-accent); margin-bottom: 4px;">${t('analysis.role_tool_call') || '工具调用 (Tool Call)'}</div>
            ${cards}
          </div>
        `;
      }
      bodyHtml = reasoningHtml + toolHtml + textHtml;
    }
  } else {
    bodyHtml = genLocalCollapsibleHtml(item.preview || '', contentField);
  }

  // 构造工具加载标签及列表面板
  let toolsBadgeHtml = '';
  let toolsPanelHtml = '';
  if (role === 'user' && item.tools && item.tools.length > 0) {
    const toolBadge = t('analysis.tool_calls_badge', { count: item.tools.length }) || `调用工具：${item.tools.length}`;
    toolsBadgeHtml = `<span class="badge" style="margin-left: 8px; cursor: pointer; background: var(--yorha-accent); color: #fff; padding: 1px 6px; font-size: 10px; font-weight: bold; border-radius: 2px;" onclick="const panel = this.closest('.yorha-msg-bubble').querySelector('.yorha-tools-panel'); panel.style.display = panel.style.display === 'none' ? 'block' : 'none';">${toolBadge}</span>`;
    
    const toolItemsHtml = item.tools.map(tool => `
      <div class="yorha-tool-item" style="margin-bottom: 6px;">
        <div class="yorha-tool-name" style="font-weight: bold; cursor: pointer; color: var(--yorha-accent); font-size: 11px; font-family: monospace;" onclick="const desc = this.nextElementSibling; desc.style.display = desc.style.display === 'none' ? 'block' : 'none';">
          • ${escapeHtml(tool.name)}
        </div>
        <div class="yorha-tool-desc" style="display: none; margin-left: 12px; margin-top: 2px; font-size: 11px; color: var(--yorha-fg-dim); white-space: pre-wrap; font-family: monospace; border-left: 1px dashed var(--yorha-border); padding-left: 6px;">
          ${escapeHtml(tool.description || (t('analysis.no_tool_desc') || '暂无工具描述'))}
        </div>
      </div>
    `).join('');

    toolsPanelHtml = `
      <div class="yorha-tools-panel" style="display: none; margin: 8px 0; padding: 8px; border: 1px dashed var(--yorha-accent); border-radius: 3px; background: rgba(205,90,63,0.02);">
        <div style="font-size: 10px; color: var(--yorha-fg-dim); margin-bottom: 6px; font-weight: bold; text-transform: uppercase;">${t('analysis.available_tools') || '加载的可用工具列表 (可用 Tools)'}</div>
        ${toolItemsHtml}
      </div>
    `;
  }

  node.innerHTML = `
    <div class="${useNoticeStyle ? 'system-notice-title' : 'bubble-meta'}" ${useNoticeStyle ? 'style="color:var(--yorha-accent)"' : ''}>
      <span>${roleName}</span>${item.turn_id ? `<span class="request-seq-badge" title="${t('analysis.request_seq_title') || '所属请求轮次'}">#${item.turn_id}</span>` : ''}
      ${toolsBadgeHtml}
    </div>
    ${toolsPanelHtml}
    ${bodyHtml}
  `;
  return node;
}

function genLocalCollapsibleHtml(text, field = 'content') {
  const safeText = escapeHtml(text || (t('analysis.empty_content') || '（空内容）'));
  const isTool = field === 'tool_calls' || field === 'tool_result';
  const limit = isTool ? 200 : 300;
  const contentStyle = isTool ? 'font-family:monospace; font-size:11px; word-break:break-all;' : '';
  const contentClass = isTool ? '' : 'yorha-msg-content';
  if (text.length <= limit) {
    return `<div class="${contentClass}" style="white-space:pre-wrap; ${contentStyle}">${safeText}</div>`;
  }
  const shortText = escapeHtml(text.substring(0, limit));
  const labelStyle = 'color: var(--yorha-accent); font-weight: bold; cursor: pointer; display: block; margin-top: 6px;';
  return `
    <div class="collapsible-text-container" style="${contentStyle}">
      <div class="text-collapsed">
        <div class="${contentClass}" style="white-space:pre-wrap; ${contentStyle}">${shortText}...</div>
        <span style="${labelStyle}" onclick="this.parentElement.style.display='none'; this.parentElement.nextElementSibling.style.display='block';">${t('analysis.expand') || '[ 展 开 ]'}</span>
      </div>
      <div class="text-expanded" style="display:none;">
        <div class="${contentClass}" style="white-space:pre-wrap; ${contentStyle}">${safeText}</div>
        <span style="${labelStyle}" onclick="this.parentElement.style.display='none'; this.parentElement.previousElementSibling.style.display='block';">${t('analysis.collapse') || '[ 收 起 ]'}</span>
      </div>
    </div>
  `;
}

function renderLocalToolCallCard(tc) {
  return `
    <div style="padding: 6px 8px; margin-top:4px; background: rgba(0,0,0,0.04); border: 1px solid var(--yorha-border); border-radius: 3px;">
      <div style="font-family:monospace; font-size:11px; color:var(--yorha-accent); word-break:break-all;">${escapeHtml(tc)}</div>
    </div>
  `;
}

// ── 渲染会话诊断的时间线对话气泡 ──
function renderChatTimeline(turns) {
  window._currentTimelineTurns = turns; // 暂存全局
  const timeline = $('chatTimeline');
  timeline.innerHTML = '';

  const tids = Object.keys(turns).map(Number).sort((a,b) => a - b);
  tids.forEach(tid => {
    const turn = turns[tid];

    if (turn.chat_history && turn.chat_history.length > 0) {
      // 系统提示词单独列出：有 system 消息则显示在该 turn 用户消息上方；
      // 拼接模式下 chat_history 中不存在 system 角色消息，自然不显示。
      const systemMsgs = turn.chat_history.filter(m => m.role === 'system');
      if (systemMsgs.length > 0) {
        const sysBlock = document.createElement('div');
        sysBlock.className = 'yorha-system-prompt-block';
        sysBlock.style.cssText = 'margin-bottom: 12px; border: 1px solid var(--yorha-accent); border-radius: 4px; overflow: hidden;';
        const sysHeader = document.createElement('div');
        sysHeader.style.cssText = 'padding: 6px 10px; background: rgba(205,90,63,0.08); font-size: 11px; font-weight: 600; color: var(--yorha-accent); display: flex; align-items: center; gap: 6px;';
        sysHeader.innerHTML = `<span>📋 ${t('analysis.system_prompt_label') || '系统提示词 (System Prompt)'}</span>` + (systemMsgs.length > 1 ? `<span style="font-weight:400;opacity:.7;">×${systemMsgs.length}</span>` : '');
        sysBlock.appendChild(sysHeader);
        const sysBody = document.createElement('div');
        sysBody.style.cssText = 'padding: 8px 10px;';
        systemMsgs.forEach((m, i) => {
          const txt = m.content || (t('analysis.empty_content') || '（空内容）');
          const origIdx = turn.chat_history.indexOf(m);
          sysBody.insertAdjacentHTML('beforeend', genCollapsibleHtml(txt, tid, 'content', origIdx));
          if (i < systemMsgs.length - 1) {
            sysBody.insertAdjacentHTML('beforeend', '<hr style="border:none;border-top:1px dashed var(--yorha-border);margin:8px 0;">');
          }
        });
        sysBlock.appendChild(sysBody);
        timeline.appendChild(sysBlock);
      }

      turn.chat_history.forEach((msg, mIdx) => {
        if (msg.role === 'system') return;
        if (msg.role === 'user') {
          const uBubble = document.createElement('div');
          uBubble.className = 'yorha-msg-bubble user';
          const roleName = t('analysis.role_user') || '用户';
          
          const txt = msg.content || (t('analysis.empty_content') || '（空内容）');
          let bodyHtml = genCollapsibleHtml(txt, tid, 'content', mIdx);
          
          let contentHtml = `
            <div class="bubble-meta">
              <span>${roleName}</span>
            </div>
            ${bodyHtml}
          `;

          const isLastUser = (msg.role === 'user' && !turn.chat_history.slice(mIdx + 1).some(m => m.role === 'user'));
          if (isLastUser && (turn.system_modified || turn.extra_injected)) {
            const primaryEvent = turn.events.find(e => e.phase === 'primary');
            
            let detailsHtml = "";
            if (turn.system_modified) detailsHtml += (t('analysis.optimization_recombined') || "• System Prompt 拼接位置已重组优化") + "\n";
            if (turn.extra_injected) detailsHtml += (t('analysis.optimization_extra') || "• 注入了额外定义的最高优先级指令") + "\n";
            const snapLabel = t('analysis.config_snapshot') || '[配置快照]:';
            const tmLabel = t('analysis.thinking_mode_label') || '思考模式';
            const reLabel = t('analysis.reasoning_effort_label') || '推理强度';
            const mtLabel = t('analysis.max_tokens_label') || 'MaxTokens';
            const def = t('analysis.default') || '默认';
            const unlim = t('analysis.unlimited') || '不限制';
            if (primaryEvent && primaryEvent.request) {
              detailsHtml += `\n${snapLabel}\n- ${tmLabel}: ${primaryEvent.request.thinking_mode || def}\n- ${reLabel}: ${primaryEvent.request.reasoning_effort || def}\n- ${mtLabel}: ${primaryEvent.request.max_tokens || unlim}`;
            }

            contentHtml += `
              <div class="log-chip">
                <div class="log-chip-title" onclick="toggleChip(this)">SYSTEM OPTIMIZATION</div>
                <div class="log-chip-content" style="white-space:pre-wrap;">${escapeHtml(detailsHtml)}</div>
              </div>
            `;
          }

          uBubble.innerHTML = contentHtml;
          timeline.appendChild(uBubble);

        } else if (msg.role === 'tool') {
          const notice = document.createElement('div');
          notice.className = 'system-notice success';
          notice.style.borderColor = 'var(--yorha-accent)';
          notice.style.background = 'rgba(205,90,63,0.03)';
          
          const rawStr = msg.content || '';
          let resultBodyHtml = genCollapsibleHtml(rawStr, tid, 'content', mIdx);

          const roleTR = t('analysis.role_tool_result') || '工具返回内容 (Tool Result)';
          notice.innerHTML = `<div class="system-notice-title" style="color:var(--yorha-accent)"><span>${roleTR}</span></div>${resultBodyHtml}`;
          timeline.appendChild(notice);

        } else if (msg.role === 'assistant') {
          const hasTools = msg.tool_calls && msg.tool_calls.length > 0;
          const hasContent = !!msg.content;
          
          const primaryEvent = turn.events.find(e => e.phase === 'primary');
          const logId = primaryEvent ? primaryEvent.log_id : 0;

          if (hasTools && !hasContent) {
            const notice = document.createElement('div');
            notice.className = 'system-notice success';
            notice.style.borderColor = 'var(--yorha-accent)';
            notice.style.background = 'rgba(205,90,63,0.03)';
            
            let contentHtml = `
              <div class="system-notice-title" style="color:var(--yorha-accent); display:flex; justify-content:space-between; align-items:center;">
                <span>${t('analysis.role_tool_call') || '调用工具内容 (Tool Call)'}</span>
                ${logId ? `<span class="jump-raw-btn" onclick="jumpToRawLog(${logId})" style="margin-left:auto;">[原始数据]</span>` : ''}
              </div>
            `;
            
            if (msg.reasoning_content) {
              const rLen = msg.reasoning_content.length;
              contentHtml += `
                <div class="log-chip" data-turn-id="${tid}" data-field="reasoning_content" data-history-idx="${mIdx}">
                  <div class="log-chip-title" onclick="toggleChip(this)">DEEPSEEK THINKING PATH (点击展开/折叠 ${rLen} 字)</div>
                  <div class="log-chip-content" style="white-space:pre-wrap;"></div>
                </div>
              `;
            }
            
            const toolRefs = msg.tool_call_refs || [];
            const toolCallBodyHtml = msg.tool_calls.map((tc, idx) => {
              const ref = toolRefs[idx] || null;
              return renderToolCallCard(tc, ref, currentSessionId);
            }).join('');
            
            contentHtml += `<div style="margin-top:6px;">${toolCallBodyHtml}</div>`;
            notice.innerHTML = contentHtml;
            timeline.appendChild(notice);
          } else {
            const aBubble = document.createElement('div');
            aBubble.className = 'yorha-msg-bubble assistant';
            
            let contentHtml = `
              <div class="bubble-meta">
                <span>${t('analysis.role_ai') || 'AI 响应'}</span>
                ${logId ? `<span class="jump-raw-btn" onclick="jumpToRawLog(${logId})">[原始数据]</span>` : ''}
              </div>
            `;

            if (msg.reasoning_content) {
              const rLen = msg.reasoning_content.length;
              contentHtml += `
                <div class="log-chip" data-turn-id="${tid}" data-field="reasoning_content" data-history-idx="${mIdx}">
                  <div class="log-chip-title" onclick="toggleChip(this)">DEEPSEEK THINKING PATH (点击展开/折叠 ${rLen} 字)</div>
                  <div class="log-chip-content" style="white-space:pre-wrap;"></div>
                </div>
              `;
            }

            if (hasTools) {
              const toolRefs = msg.tool_call_refs || [];
              const toolCallBodyHtml = msg.tool_calls.map((tc, idx) => {
                const ref = toolRefs[idx] || null;
                return renderToolCallCard(tc, ref, currentSessionId);
              }).join('');
              contentHtml += `<div style="margin-bottom:10px; padding: 10px; border-left: 3px solid var(--yorha-accent); background: rgba(0,0,0,0.05);"><div style="font-weight:600; font-size:11px; color: var(--yorha-accent); margin-bottom: 4px;">${t('analysis.role_tool_call') || '工具调用 (Tool Call)'}</div>${toolCallBodyHtml}</div>`;
            }

            if (msg.content) {
              const txt = msg.content;
              let bodyHtml = genCollapsibleHtml(txt, tid, 'content', mIdx);
              contentHtml += bodyHtml;
            } else {
              contentHtml += `<div style="color:var(--yorha-fg-dim); font-style:italic;">${t('analysis.no_text_output') || '仅包含思考或工具调用，无文本输出'}</div>`;
            }

            aBubble.innerHTML = contentHtml;
            timeline.appendChild(aBubble);
          }
        }
      });

      turn.events.forEach(ev => {
        if (ev.phase === 'analyzer') {
          const notice = document.createElement('div');
          notice.className = 'system-notice danger';
          notice.innerHTML = `
            <div class="system-notice-title">
              <span>${t('analysis.analyzer_triggered') || '系统：并行防无限思考分析已触发'}</span>
              <span class="jump-raw-btn" onclick="jumpToRawLog(${ev.log_id})">[原始数据]</span>
            </div>
            <div>${t('analysis.analyzer_detected', { reason: ev.response.finish_reason || '', judgment: ev.response.analyzer_judgment || '' }) || `检测到模型可能陷入无限思考（完成状态: <b>${ev.response.finish_reason}</b>）。<br>分析器判定：<b>${ev.response.analyzer_judgment}</b>`}</div>
          `;
          timeline.appendChild(notice);
        } else if (ev.phase === 'retry') {
          const notice = document.createElement('div');
          notice.className = 'system-notice success';
          notice.innerHTML = `
            <div class="system-notice-title">
              <span>${t('analysis.retry_executed') || '系统：反无限思考重试已执行'}</span>
              <span class="jump-raw-btn" onclick="jumpToRawLog(${ev.log_id})">[原始数据]</span>
            </div>
            <div>${t('analysis.retry_detail', { model: ev.response.retry_model || '', latency: ev.latency_ms || '', status: ev.response.finish_reason || '' }) || `代理已强制重新整理思路并进行二次重试。使用模型：<b>${ev.response.retry_model}</b>，耗时: <b>${ev.latency_ms}ms</b>，重试状态：<b>${ev.response.finish_reason}</b>`}</div>
          `;
          timeline.appendChild(notice);
        }
      });

    } else {
      // 兼容旧的 session
      if (turn.user_message) {
        const uBubble = document.createElement('div');
        uBubble.className = 'yorha-msg-bubble user';
        
        const txt = turn.user_message;
        let bodyHtml = genCollapsibleHtml(txt, tid, 'user_message');
        
        let contentHtml = `
          <div class="bubble-meta">
            <span>${t('analysis.user_turn', { id: turn.turn_id }) || `用户 (轮次 #${turn.turn_id})`}</span>
          </div>
          ${bodyHtml}
        `;

        if (turn.system_modified || turn.extra_injected) {
          const primaryEvent = turn.events.find(e => e.phase === 'primary');
          
          let detailsHtml = "";
          if (turn.system_modified) detailsHtml += (t('analysis.optimization_recombined') || "• System Prompt 拼接位置已重组优化") + "\n";
          if (turn.extra_injected) detailsHtml += (t('analysis.optimization_extra') || "• 注入了额外定义的最高优先级指令") + "\n";
          if (primaryEvent && primaryEvent.request) {
            const snapL = t('analysis.config_snapshot') || '[配置快照]:';
            const tmL = t('analysis.thinking_mode_label') || '思考模式';
            const reL = t('analysis.reasoning_effort_label') || '推理强度';
            const mtL = t('analysis.max_tokens_label') || 'MaxTokens';
            const dL = t('analysis.default') || '默认';
            const uL = t('analysis.unlimited') || '不限制';
            detailsHtml += `\n${snapL}\n- ${tmL}: ${primaryEvent.request.thinking_mode || dL}\n- ${reL}: ${primaryEvent.request.reasoning_effort || dL}\n- ${mtL}: ${primaryEvent.request.max_tokens || uL}`;
          }

          contentHtml += `
            <div class="log-chip">
              <div class="log-chip-title" onclick="toggleChip(this)">SYSTEM OPTIMIZATION</div>
              <div class="log-chip-content" style="white-space:pre-wrap;">${escapeHtml(detailsHtml)}</div>
            </div>
          `;
        }

        uBubble.innerHTML = contentHtml;
        timeline.appendChild(uBubble);
      }

      turn.events.forEach(ev => {
        if (ev.phase === 'analyzer') {
          const notice = document.createElement('div');
          notice.className = 'system-notice danger';
          notice.innerHTML = `
            <div class="system-notice-title">
              <span>${t('analysis.analyzer_triggered') || '系统：并行防无限思考分析已触发'}</span>
              <span class="jump-raw-btn" onclick="jumpToRawLog(${ev.log_id})">[原始数据]</span>
            </div>
            <div>${t('analysis.analyzer_detected', { reason: ev.response.finish_reason || '', judgment: ev.response.analyzer_judgment || '' }) || `检测到模型可能陷入无限思考（完成状态: <b>${ev.response.finish_reason}</b>）。<br>分析器判定：<b>${ev.response.analyzer_judgment}</b>`}</div>
          `;
          timeline.appendChild(notice);
        } else if (ev.phase === 'retry') {
          const notice = document.createElement('div');
          notice.className = 'system-notice success';
          notice.innerHTML = `
            <div class="system-notice-title">
              <span>${t('analysis.retry_executed') || '系统：反无限思考重试已执行'}</span>
              <span class="jump-raw-btn" onclick="jumpToRawLog(${ev.log_id})">[原始数据]</span>
            </div>
            <div>${t('analysis.retry_detail', { model: ev.response.retry_model || '', latency: ev.latency_ms || '', status: ev.response.finish_reason || '' }) || `代理已强制重新整理思路并进行二次重试。使用模型：<b>${ev.response.retry_model}</b>，耗时: <b>${ev.latency_ms}ms</b>，重试状态：<b>${ev.response.finish_reason}</b>`}</div>
          `;
          timeline.appendChild(notice);
        }
      });

      if (turn.assistant_response || turn.reasoning_content || (turn.tool_calls && turn.tool_calls.length > 0)) {
        const hasTools = turn.tool_calls && turn.tool_calls.length > 0;
        const hasContent = !!turn.assistant_response;
        
        const primaryEvent = turn.events.find(e => e.phase === 'primary');
        const logId = primaryEvent ? primaryEvent.log_id : 0;

        if (hasTools && !hasContent) {
          const notice = document.createElement('div');
          notice.className = 'system-notice success';
          notice.style.borderColor = 'var(--yorha-accent)';
          notice.style.background = 'rgba(205,90,63,0.03)';
          
          let contentHtml = `
            <div class="system-notice-title" style="color:var(--yorha-accent); display:flex; justify-content:space-between; align-items:center;">
              <span>调用工具内容 (Tool Call)</span>
              ${logId ? `<span class="jump-raw-btn" onclick="jumpToRawLog(${logId})" style="margin-left:auto;">[原始数据]</span>` : ''}
            </div>
          `;
          
          if (turn.reasoning_content) {
            const rLen = turn.reasoning_content.length;
            contentHtml += `
              <div class="log-chip" data-turn-id="${tid}" data-field="reasoning_content">
                <div class="log-chip-title" onclick="toggleChip(this)">DEEPSEEK THINKING PATH (点击展开/折叠 ${rLen} 字)</div>
                <div class="log-chip-content" style="white-space:pre-wrap;"></div>
              </div>
            `;
          }
          
          const toolRefs = turn.tool_call_refs || [];
          const toolCallBodyHtml = turn.tool_calls.map((tc, idx) => {
            const ref = toolRefs[idx] || null;
            return renderToolCallCard(tc, ref, currentSessionId);
          }).join('');
          
          contentHtml += `<div style="margin-top:6px;">${toolCallBodyHtml}</div>`;
          notice.innerHTML = contentHtml;
          timeline.appendChild(notice);
        } else {
          const aBubble = document.createElement('div');
          aBubble.className = 'yorha-msg-bubble assistant';
          
          let contentHtml = `
            <div class="bubble-meta">
              <span>AI 响应</span>
              ${logId ? `<span class="jump-raw-btn" onclick="jumpToRawLog(${logId})">[原始数据]</span>` : ''}
            </div>
          `;

          if (turn.reasoning_content) {
            const rLen = turn.reasoning_content.length;
            contentHtml += `
              <div class="log-chip" data-turn-id="${tid}" data-field="reasoning_content">
                <div class="log-chip-title" onclick="toggleChip(this)">DEEPSEEK THINKING PATH (点击展开/折叠 ${rLen} 字)</div>
                <div class="log-chip-content" style="white-space:pre-wrap;"></div>
              </div>
            `;
          }

          if (hasTools) {
            const toolRefs = turn.tool_call_refs || [];
            const toolCallBodyHtml = turn.tool_calls.map((tc, idx) => {
              const ref = toolRefs[idx] || null;
              return renderToolCallCard(tc, ref, currentSessionId);
            }).join('');
            contentHtml += `<div style="margin-bottom:10px; padding: 10px; border-left: 3px solid var(--yorha-accent); background: rgba(0,0,0,0.05);"><div style="font-weight:600; font-size:11px; color: var(--yorha-accent); margin-bottom: 4px;">工具调用 (Tool Call)</div>${toolCallBodyHtml}</div>`;
          }

          if (turn.assistant_response) {
            const txt = turn.assistant_response;
            let bodyHtml = genCollapsibleHtml(txt, tid, 'assistant_response');
            contentHtml += bodyHtml;
          } else {
            contentHtml += `<div style="color:var(--yorha-fg-dim); font-style:italic;">${t('analysis.no_text_output') || '仅包含思考或工具调用，无文本输出'}</div>`;
          }

          aBubble.innerHTML = contentHtml;
          timeline.appendChild(aBubble);
        }
      }
    }
  });
}

function toggleChip(header) {
  const parent = header.parentElement;
  if (parent) {
    const isOpen = parent.classList.toggle('open');
    const contentEl = parent.querySelector('.log-chip-content');
    if (contentEl && parent.dataset.turnId) {
      if (isOpen) {
        const tid = parseInt(parent.dataset.turnId);
        const field = parent.dataset.field;
        const hIdx = parent.dataset.historyIdx !== undefined ? parseInt(parent.dataset.historyIdx) : -1;
        const text = getTimelineText(tid, field, hIdx);
        contentEl.textContent = text;
      } else {
        contentEl.textContent = '';
      }
    }
  }
}

function getTimelineText(tid, field, hIdx) {
  const turns = window._currentTimelineTurns;
  if (!turns) return "";
  const turn = turns[tid];
  if (!turn) return "";

  if (hIdx >= 0 && turn.chat_history && turn.chat_history[hIdx]) {
    const msg = turn.chat_history[hIdx];
    if (field === 'content') return msg.content || "";
    if (field === 'reasoning_content') return msg.reasoning_content || "";
    if (field === 'tool_calls') return formatToolCallsText(msg.tool_calls);
  }

  if (field === 'user_message') return turn.user_message || "";
  if (field === 'assistant_response') return turn.assistant_response || "";
  if (field === 'reasoning_content') return turn.reasoning_content || "";
  if (field === 'tool_calls') return formatToolCallsText(turn.tool_calls);

  return "";
}

// 把 ToolCall 数组格式化为可读文本（每个工具一段，参数美化 JSON）
function formatToolCallsText(toolCalls) {
  if (!toolCalls || !toolCalls.length) return "";
  return toolCalls.map(tc => {
    if (typeof tc === 'string') return tc;
    let inputPretty = '';
    if (tc.input) {
      try { inputPretty = JSON.stringify(JSON.parse(tc.input), null, 2); }
      catch(e) { inputPretty = tc.input; }
    }
    const idSuffix = tc.id ? `  [id: ${tc.id}]` : '';
    return `${tc.name || '(未知)'}${idSuffix}\n${inputPretty}`;
  }).join('\n\n');
}

function genCollapsibleHtml(text, turnId, field, historyIdx = -1) {
  const isHistory = historyIdx >= 0;
  const isTool = field === 'tool_calls' || field === 'tool_result';
  const limit = isTool ? 200 : 300;
  
  if (text.length <= limit) {
    if (isTool) {
      return `<div style="font-family:monospace; font-size:11px; white-space:pre-wrap; word-break:break-all;">${escapeHtml(text)}</div>`;
    }
    return `<div class="yorha-msg-content">${escapeHtml(text)}</div>`;
  }
  
  const shortText = text.substring(0, limit);
  const dataAttrs = `data-turn-id="${turnId}" data-field="${field}" ${isHistory ? `data-history-idx="${historyIdx}"` : ''}`;
  const contentStyle = isTool ? 'font-family:monospace; font-size:11px; word-break:break-all;' : '';
  const labelStyle = isTool ? 'font-weight: bold; font-size: 10px;' : 'color: var(--yorha-accent); font-weight: bold;';
  
  return `
    <div class="collapsible-text-container" style="cursor: pointer; ${contentStyle}" ${dataAttrs} onclick="toggleTextCollapse(this, event)">
      <div class="text-collapsed">
        <div class="${isTool ? '' : 'yorha-msg-content'}" style="white-space:pre-wrap;">${escapeHtml(shortText)}...</div>
        <span style="${labelStyle} display: block; margin-top: 6px;">${t('analysis.expand') || '[ 展 开 ]'}</span>
      </div>
    </div>
  `;
}

function exportSessionMarkdown() {
  if (!currentSessionId) return;
  openExportModal();
}

// ── 导出诊断报告配置弹窗 ──
let currentExportTemplate = 'full';

function defaultExportConfig() {
  return {
    system_prompt: true, user_message: true, tool_prompt: true,
    reasoning: true, tool_call: true, tool_result: true, content: true,
    tool_result_max_len: 0
  };
}

function liteExportConfig() {
  return {
    system_prompt: true, user_message: true, tool_prompt: false,
    reasoning: false, tool_call: true, tool_result: true, content: true,
    tool_result_max_len: 200
  };
}

function openExportModal() {
  if (!currentSessionId) return;
  // 将模板下拉框转换为 yorha 自定义组件，并默认选中「默认（全部）」
  convertSelectsToYorha('#exportModal');
  const tplSelect = $('exportTemplateSelect');
  if (tplSelect) {
    tplSelect.value = 'full';
    tplSelect.dispatchEvent(new Event('change'));
  } else {
    selectExportTemplate('full');
  }
  const modal = $('exportModal');
  modal.style.display = 'flex';
  const serial = $('exportSerial');
  if (serial) serial.textContent = 'ID: ' + (currentSessionId || '----');
  requestAnimationFrame(() => modal.classList.add('open'));
}

function closeExportModal() {
  const modal = $('exportModal');
  modal.classList.remove('open');
  modal.style.display = 'none';
}

function selectExportTemplate(tpl) {
  currentExportTemplate = tpl;
  const cfg = tpl === 'lite' ? liteExportConfig() : defaultExportConfig();
  document.querySelectorAll('#exportModal input[data-sec]').forEach(cb => {
    cb.checked = !!cfg[cb.dataset.sec];
  });
  const hint = $('exportHint');
  if (hint) {
    if (tpl === 'lite') {
      hint.textContent = t('analysis.export_lite_hint') || '精简模板：仅导出系统提示词、用户消息、回复正文、工具调用命令，工具返回内容摘要为前 200 字。';
    } else {
      hint.textContent = t('analysis.export_full_hint') || '默认模板：导出全部内容板块。';
    }
  }
}

async function confirmExport() {
  if (!currentSessionId) return;
  const cfg = defaultExportConfig();
  document.querySelectorAll('#exportModal input[data-sec]').forEach(cb => {
    cfg[cb.dataset.sec] = cb.checked;
  });
  if (currentExportTemplate === 'lite') {
    cfg.tool_result_max_len = 200;
  }

  try {
    const r = await fetch(`/api/analysis/sessions/${currentSessionId}/export.md`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(cfg)
    });
    if (!r.ok) throw new Error('export failed');
    const md = await r.text();
    const blob = new Blob([md], { type: 'text/markdown;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `dsplus_session_${currentSessionId}.md`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
    closeExportModal();
    triggerAlert(t('analysis.export_done') || '诊断报告已导出');
  } catch (e) {
    triggerAlert(t('analysis.export_failed') || '导出失败，请重试');
  }
}

async function jumpToRawLog(logId) {
  if (!logId) return;
  const dashboardTab = document.querySelector('.yorha-nav button[data-page="dashboard"]');
  if (dashboardTab) {
    dashboardTab.click();
  }
  let exists = logs.some(l => l.id === logId);
  if (!exists) {
    await loadLogs();
  }
  showDetail(logId);
}

// ── 数字微调步进器 ──
function stepValue(inputId, delta, min, max) {
  const el = document.getElementById(inputId);
  if (!el) return;
  let val = parseInt(el.value) || 0;
  val += delta;
  if (min !== undefined && val < min) val = min;
  if (max !== undefined && val > max) val = max;
  el.value = val;
  el.dispatchEvent(new Event('change'));
}

// ── 寄叶自定义模拟下拉组件转换 ──
function convertSelectsToYorha(root = '#settings') {
  const selects = document.querySelectorAll(root + ' select');
  selects.forEach(select => {
    // 如果之前已经转过，我们先清理掉以支持重新回填
    let wrapper = select.parentElement;
    if (wrapper && wrapper.classList.contains('yorha-select-wrapper')) {
      const trigger = wrapper.querySelector('.yorha-select-trigger');
      const optionsContainer = wrapper.querySelector('.yorha-select-options');
      if (trigger) trigger.remove();
      if (optionsContainer) optionsContainer.remove();
      // 把 select 移出来并放回原处，然后删除包装
      wrapper.parentNode.insertBefore(select, wrapper);
      wrapper.remove();
    }

    select.style.display = 'none';

    wrapper = document.createElement('div');
    wrapper.className = 'yorha-select-wrapper';
    
    select.parentNode.insertBefore(wrapper, select);
    wrapper.appendChild(select);

    const trigger = document.createElement('div');
    trigger.className = 'yorha-select-trigger';
    const updateTriggerText = () => {
      const selectedOption = select.options[select.selectedIndex];
      trigger.textContent = selectedOption ? selectedOption.textContent : '';
    };
    updateTriggerText();
    wrapper.appendChild(trigger);

    const optionsContainer = document.createElement('div');
    optionsContainer.className = 'yorha-select-options';
    wrapper.appendChild(optionsContainer);

    const rebuildOptions = () => {
      optionsContainer.innerHTML = '';
      Array.from(select.options).forEach((opt, idx) => {
        const item = document.createElement('div');
        item.className = 'yorha-select-option-item';
        if (idx === select.selectedIndex) {
          item.classList.add('selected');
        }
        item.textContent = opt.textContent;
        item.addEventListener('click', (e) => {
          e.stopPropagation();
          select.selectedIndex = idx;
          updateTriggerText();
          wrapper.classList.remove('open');
          select.dispatchEvent(new Event('change'));
        });
        optionsContainer.appendChild(item);
      });
    };
    rebuildOptions();

    select.addEventListener('change', () => {
      updateTriggerText();
      Array.from(optionsContainer.children).forEach((child, idx) => {
        if (idx === select.selectedIndex) {
          child.classList.add('selected');
        } else {
          child.classList.remove('selected');
        }
      });
    });

    trigger.addEventListener('click', (e) => {
      e.stopPropagation();
      document.querySelectorAll('.yorha-select-wrapper').forEach(w => {
        if (w !== wrapper) w.classList.remove('open');
      });
      wrapper.classList.toggle('open');
    });
  });

  // 全局点击关闭所有下拉框（只绑定一次）
  if (!window.__yorhaClickBound) {
    document.addEventListener('click', () => {
      document.querySelectorAll('.yorha-select-wrapper').forEach(w => {
        w.classList.remove('open');
      });
    });
    window.__yorhaClickBound = true;
  }
}

// ── DOM 就绪时初始化 ──
document.addEventListener('DOMContentLoaded', () => {
  // 设置折叠联动初始处理
  const binds = [
    { trigger: 'cfgThinkingMode', target: 'effortGroup', type: 'select', cond: val => val === 'enabled' },
    { trigger: 'cfgExtraPlacement', target: 'extraPromptGroup', type: 'select', cond: val => val !== 'none' },
    { trigger: 'cfgMaxTokensMode', target: 'maxTokensCustomGroup', type: 'select', cond: val => val === 'custom' },
    { trigger: 'cfgAntiLoop', target: 'antiloopSubSettings', type: 'checkbox', cond: checked => checked },
    { trigger: 'cfgAntiloopRetryThinking', target: 'antiloopEffortGroup', type: 'select', cond: val => val === 'enabled' },
    { trigger: 'cfgAnalysisEnabled', target: 'analysisSubSettings', type: 'checkbox', cond: checked => checked }
  ];
  binds.forEach(b => {
    const trigEl = document.getElementById(b.trigger);
    const targetEl = document.getElementById(b.target);
    if (!trigEl || !targetEl) return;

    const update = () => {
      const show = b.type === 'select' ? b.cond(trigEl.value) : b.cond(trigEl.checked);
      targetEl.style.display = show ? '' : 'none';
    };

    trigEl.addEventListener('change', update);
    update();
  });

  // 主题实时联动
  const themeSelect = document.getElementById('cfgTheme');
  if (themeSelect) {
    const applyLiveTheme = () => {
      const selectedTheme = themeSelect.value;
      document.body.className = 'theme-' + selectedTheme;
    };
    themeSelect.addEventListener('change', applyLiveTheme);
  }

  // 导出模板自定义下拉框：变更时同步板块勾选
  const tplSelect = document.getElementById('exportTemplateSelect');
  if (tplSelect) {
    tplSelect.addEventListener('change', e => selectExportTemplate(e.target.value));
  }
});

// ── 开始执行 ──
init().then(() => {
  loadLogs();
  setInterval(loadStatus, 5000);
});

// ── 气泡文字折叠切换 ──
function toggleTextCollapse(container, event) {
  // 检查是否有选中的文本（拖拽框选）
  if (window.getSelection() && window.getSelection().toString().trim() !== '') {
    return;
  }
  // 检查点击防抖，如果鼠标位移大于 5 像素或按下时间超过 500ms，则判定为拖拽选择，不触发折叠
  if (event) {
    const moveX = Math.abs(event.clientX - lastMouseDownX);
    const moveY = Math.abs(event.clientY - lastMouseDownY);
    const duration = Date.now() - lastMouseDownTime;
    if (moveX > 5 || moveY > 5 || duration > 500) {
      return;
    }
  }

  const tid = parseInt(container.dataset.turnId);
  const field = container.dataset.field;
  const hIdx = container.dataset.historyIdx !== undefined ? parseInt(container.dataset.historyIdx) : -1;
  const isTool = field === 'tool_calls' || field === 'tool_result';
  const limit = isTool ? 200 : 300;
  
  const fullText = getTimelineText(tid, field, hIdx);
  const collapsedEl = container.querySelector('.text-collapsed');
  
  if (collapsedEl) {
    const labelEl = collapsedEl.querySelector('span');
    const contentInner = collapsedEl.querySelector('div');
    const isExpanded = labelEl.textContent.includes('收 起');
    
    if (isExpanded) {
      const shortText = fullText.substring(0, limit);
      contentInner.textContent = shortText + "...";
      labelEl.textContent = t('analysis.expand') || '[ 展 开 ]';
    } else {
      contentInner.textContent = fullText;
      labelEl.textContent = t('analysis.collapse') || '[ 收 起 ]';
    }
  }
}

/* ════════════════════════════════════════════════════════════════════
   ── 模型服务 (供应商管理) 前端业务与真实接口实现 ──
════════════════════════════════════════════════════════════════════ */

let modelServicesInitialized = false;
let curModelProviderIdx = 0;
let curEndpointTabType = 'openai';
let currentTestingModel = null;
let currentTestingProtocol = 'openai';
let fetchedModelsPool = [];
let fetchedSelectedSet = new Set();

async function initModelServicesPage() {
  if (providersData.length === 0) {
    await loadSettings();
  }
  if (activeProviderIndex >= 0 && activeProviderIndex < providersData.length) {
    curModelProviderIdx = activeProviderIndex;
  } else {
    curModelProviderIdx = 0;
  }
  await refreshRoutePreview(); // 进入页面时拉取一次路由表，保证冲突徽标为最新
  renderModelProvidersList();
  renderCurrentProviderDetail();
  modelServicesInitialized = true;
}

// ── 渲染左栏供应商列表（拖拽排序 + 多选启用开关） ──
let providerDragIndex = -1;

function renderModelProvidersList() {
  const container = $('modelProviderListContainer');
  if (!container) return;
  container.innerHTML = '';

  const searchKeyword = ($('modelProviderSearchInput')?.value || '').trim().toLowerCase();
  const countBadge = $('modelProviderCountBadge');
  if (countBadge) {
    const enabledCount = providersData.filter((p, i) => i === 0 || p.Enabled !== false).length;
    countBadge.textContent = `已启用 ${enabledCount} / 共 ${providersData.length}`;
  }

  if (curModelProviderIdx >= providersData.length) {
    curModelProviderIdx = Math.max(0, providersData.length - 1);
  }

  providersData.forEach((provider, idx) => {
    const pName = provider.Name || ('(未命名 ' + idx + ')');
    if (searchKeyword && !pName.toLowerCase().includes(searchKeyword)) {
      return;
    }

    const isDefault = (idx === 0);
    const isOn = isDefault || provider.Enabled !== false;
    const item = document.createElement('div');
    item.className = 'provider-list-item' + (idx === curModelProviderIdx ? ' active' : '');
    item.draggable = true;
    item.dataset.index = String(idx);

    const iconLetter = (pName.charAt(0) || 'P').toUpperCase();
    const defaultTagHtml = isDefault ? `<span class="provider-default-tag">默认</span>` : '';
    const modelsCountText = provider.Models && provider.Models.length > 0 ? `${provider.Models.length} 模型` : '未配置模型清单';
    const idText = provider.ID || '(无 ID)';

    item.innerHTML = `
      <div class="provider-drag-handle" title="拖拽调整顺序，第一位为默认供应商"><span></span><span></span><span></span></div>
      <div class="provider-icon-badge">${escapeHtml(iconLetter)}</div>
      <div class="provider-info-col">
        <div class="provider-info-name-row">
          <span class="provider-info-name" title="${escapeHtml(pName)}">${escapeHtml(pName)}</span>
          ${defaultTagHtml}
        </div>
        <div style="font-size: 10px; opacity: 0.7; font-family: monospace;">
          ${escapeHtml(idText)} · ${modelsCountText}
        </div>
      </div>
      <label class="yorha-switch" style="flex-shrink: 0;" title="${isDefault ? '默认供应商不可关闭' : '启用后参与模型路由'}" onclick="event.stopPropagation()">
        <input type="checkbox" ${isOn ? 'checked' : ''} ${isDefault ? 'disabled' : ''} onchange="toggleProviderEnabled(${idx}, this.checked)">
        <span class="yorha-slider"></span>
      </label>
    `;

    item.addEventListener('click', () => {
      if (curModelProviderIdx !== idx) {
        curModelProviderIdx = idx;
        renderModelProvidersList();
        renderCurrentProviderDetail();
      }
    });

    attachProviderDragEvents(item, idx);
    container.appendChild(item);
  });
}

// ── 拖拽排序：拖拽期间暂停自动保存，落位后统一提交一次，避免顺序抖动 ──
function attachProviderDragEvents(item, idx) {
  item.addEventListener('dragstart', (e) => {
    providerDragIndex = idx;
    item.classList.add('dragging');
    if (persistProvidersTimer) {
      clearTimeout(persistProvidersTimer);
      persistProvidersTimer = null;
    }
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', String(idx));
    }
  });

  item.addEventListener('dragover', (e) => {
    if (providerDragIndex < 0 || providerDragIndex === idx) return;
    e.preventDefault();
    const rect = item.getBoundingClientRect();
    const after = (e.clientY - rect.top) > rect.height / 2;
    item.classList.toggle('drop-after', after);
    item.classList.toggle('drop-before', !after);
  });

  item.addEventListener('dragleave', () => {
    item.classList.remove('drop-before', 'drop-after');
  });

  item.addEventListener('drop', (e) => {
    e.preventDefault();
    if (providerDragIndex < 0 || providerDragIndex === idx) return;
    const rect = item.getBoundingClientRect();
    const after = (e.clientY - rect.top) > rect.height / 2;
    moveProvider(providerDragIndex, idx, after);
  });

  item.addEventListener('dragend', () => {
    providerDragIndex = -1;
    document.querySelectorAll('.provider-list-item').forEach(el => {
      el.classList.remove('dragging', 'drop-before', 'drop-after');
    });
  });
}

async function moveProvider(from, to, after) {
  if (from < 0 || from >= providersData.length) return;
  const [moved] = providersData.splice(from, 1);
  let target = after ? to + 1 : to;
  if (from < target) target--;
  target = Math.max(0, Math.min(target, providersData.length));
  providersData.splice(target, 0, moved);

  activeProviderIndex = 0;
  curModelProviderIdx = target;
  fillProviderEditor();
  updateDefaultProviderLabel();
  renderModelProvidersList();
  renderCurrentProviderDetail();
  await persistProvidersConfig(true);
  triggerAlert(`顺序已更新，默认供应商：[ ${providersData[0] ? providersData[0].Name : ''} ]`);
}

function filterProvidersListUI() {
  renderModelProvidersList();
}

// ── 启用开关：关闭的供应商不参与模型路由（第一位为默认供应商，不可关闭） ──
async function toggleProviderEnabled(idx, checked) {
  const p = providersData[idx];
  if (!p) return;
  if (idx === 0) {
    p.Enabled = true;
    renderModelProvidersList();
    triggerAlert('默认供应商不可关闭');
    return;
  }
  p.Enabled = !!checked;
  renderModelProvidersList();
  await persistProvidersConfig(true);
  renderCurrentProviderDetail();
  triggerAlert(checked ? `供应商 [ ${p.Name} ] 已启用` : `供应商 [ ${p.Name} ] 已停用，不再参与路由`);
}

// ── 渲染右栏当前选中供应商详情 ──
function renderCurrentProviderDetail() {
  const p = providersData[curModelProviderIdx];
  if (!p) return;

  $('curProviderNameInput').value = p.Name || '';
  const idInput = $('curProviderIdInput');
  if (idInput) idInput.value = p.ID || '';
  $('curProviderApiKey').value = p.APIKey || '';
  $('curProviderBaseUrl').value = p.BaseURL || '';
  $('curProviderAnthropicUrl').value = p.AnthropicBaseURL || '';

  const chip = $('providerPingStatusChip');
  if (chip) chip.style.display = 'none';

  updateEndpointPreview();
  switchEndpointTab(curEndpointTabType);
  renderProviderModelsList();
}

function onCurProviderNameChange(val) {
  const p = providersData[curModelProviderIdx];
  if (!p) return;
  p.Name = val;

  const activeItem = document.querySelector('.provider-list-item.active .provider-info-name');
  if (activeItem) activeItem.textContent = val;

  if (curModelProviderIdx === 0) {
    updateDefaultProviderLabel();
    fillProviderEditor();
  }

  debouncePersistProviders(800);
}

// 供应商 ID 输入：实时校验字符与全局唯一性，非法值不写回数据
function onCurProviderIdChange(val) {
  const p = providersData[curModelProviderIdx];
  if (!p) return;
  const hint = $('curProviderIdHint');
  const trimmed = (val || '').trim();

  if (!isValidProviderId(trimmed)) {
    if (hint) {
      hint.textContent = 'ID 不能为空，且仅允许字母、数字、下划线、短横线';
      hint.style.color = 'var(--yorha-accent)';
    }
    return;
  }
  const conflictIdx = findProviderIdConflict(trimmed, curModelProviderIdx);
  if (conflictIdx >= 0) {
    if (hint) {
      hint.textContent = `该 ID 已被供应商 [ ${providersData[conflictIdx].Name} ] 使用，请更换`;
      hint.style.color = 'var(--yorha-accent)';
    }
    return;
  }

  p.ID = trimmed;
  if (hint) {
    hint.textContent = '模型 ID 在多个供应商间重名时，对外模型名会显示为 [ID/模型ID]；ID 仅允许字母、数字、下划线、短横线，不区分大小写';
    hint.style.color = '';
  }
  if (curModelProviderIdx === 0) updateDefaultProviderLabel();
  renderModelProvidersList();
  debouncePersistProviders(600);
}

function syncAndAutoSaveProviderData() {
  const p = providersData[curModelProviderIdx];
  if (!p) return;
  p.APIKey = $('curProviderApiKey').value.trim();
  p.BaseURL = $('curProviderBaseUrl').value.trim();
  p.AnthropicBaseURL = $('curProviderAnthropicUrl').value.trim();
  updateEndpointPreview();

  if (curModelProviderIdx === activeProviderIndex) {
    fillProviderEditor();
  }

  debouncePersistProviders(800);
}

function updateEndpointPreview() {
  const base = ($('curProviderBaseUrl').value || 'https://api.example.com').replace(/\/+$/, '');
  const anthropic = ($('curProviderAnthropicUrl').value || base).replace(/\/+$/, '');

  const pOpenAI = $('previewOpenAI');
  const pAnthropic = $('previewAnthropic');
  if (pOpenAI) pOpenAI.textContent = `${base}/v1/chat/completions`;
  if (pAnthropic) pAnthropic.textContent = `${anthropic}/v1/messages`;
}

// ── 协议 Tab 切换 ──
function switchEndpointTab(type) {
  curEndpointTabType = type;
  const tabOpenAI = $('tabOpenAI');
  const tabAnthropic = $('tabAnthropic');
  const paneOpenAI = $('paneOpenAI');
  const paneAnthropic = $('paneAnthropic');

  if (type === 'openai') {
    tabOpenAI?.classList.add('active');
    tabAnthropic?.classList.remove('active');
    if (paneOpenAI) paneOpenAI.style.display = '';
    if (paneAnthropic) paneAnthropic.style.display = 'none';
  } else {
    tabOpenAI?.classList.remove('active');
    tabAnthropic?.classList.add('active');
    if (paneOpenAI) paneOpenAI.style.display = 'none';
    if (paneAnthropic) paneAnthropic.style.display = '';
  }
}

// ── 删除当前选中的供应商 ──
async function deleteCurrentModelProvider() {
  if (providersData.length <= 1) {
    triggerAlert(t('settings.provider_delete_last') || '至少保留一个供应商配置');
    return;
  }
  const targetName = providersData[curModelProviderIdx].Name;
  if (!confirm(`确认删除供应商 [ ${targetName} ] 吗？`)) return;

  providersData.splice(curModelProviderIdx, 1);

  curModelProviderIdx = Math.max(0, Math.min(curModelProviderIdx, providersData.length - 1));
  activeProviderIndex = 0; // 删除后第一位可能易主，默认供应商始终跟随列表第一位
  providersData.forEach(p => { if (!p.ID) p.ID = generateProviderId(p.Name); });

  fillProviderEditor();
  updateDefaultProviderLabel();
  renderModelProvidersList();
  renderCurrentProviderDetail();

  await persistProvidersConfig(true);
  triggerAlert('供应商已移除');
}

// ── 连通性测试 (真实调用后端 /api/providers/ping) ──
async function testProviderPing() {
  const p = providersData[curModelProviderIdx];
  const btn = $('btnPingApi');
  const chip = $('providerPingStatusChip');
  if (!btn || !chip || !p) return;

  if (!p.BaseURL && !p.AnthropicBaseURL) {
    triggerAlert('请先配置基础地址 (Base URL)');
    return;
  }

  btn.disabled = true;
  btn.textContent = '检测中...';
  chip.style.display = 'none';

  try {
    const payload = {
      base_url: p.BaseURL,
      anthropic_base_url: p.AnthropicBaseURL,
      api_key: p.APIKey,
      protocol: curEndpointTabType
    };

    const res = await fetch('/api/providers/ping', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    const data = await res.json();

    chip.style.display = 'inline-block';
    if (data.ok) {
      chip.className = 'badge badge-yes';
      chip.textContent = `HTTP ${data.status} OK // 延迟: ${data.latency_ms}ms`;
    } else if (data.status > 0) {
      chip.className = 'badge badge-no';
      const statusText = data.status === 401 ? '鉴权失败 (API Key 错误)' : (data.status_text || '状态异常');
      chip.textContent = `HTTP ${data.status} // ${statusText} (${data.latency_ms}ms)`;
    } else {
      chip.className = 'badge badge-no';
      chip.textContent = `连接失败 // ${data.error || '网络不可达'}`;
    }
  } catch (err) {
    chip.style.display = 'inline-block';
    chip.className = 'badge badge-no';
    chip.textContent = `请求失败 // ${err.message || '网络异常'}`;
  } finally {
    btn.disabled = false;
    btn.textContent = '连通性测试';
  }
}

// ── 切换 API Key 明密文显示 ──
function toggleModelApiKeyVisibility() {
  const input = $('curProviderApiKey');
  const btn = input?.parentElement?.querySelector('.yorha-eye-btn');
  if (!input || !btn) return;
  if (input.type === 'password') {
    input.type = 'text';
    btn.classList.add('visible');
  } else {
    input.type = 'password';
    btn.classList.remove('visible');
  }
}

// ── 渲染模型列表 (分组折叠与穿透检测) ──
function renderProviderModelsList() {
  const p = providersData[curModelProviderIdx];
  const container = $('modelGroupsWrapper');
  const passthroughBanner = $('passthroughBanner');
  const countBadge = $('modelsCountBadge');
  if (!container || !p) return;

  container.innerHTML = '';
  const search = ($('modelSearchInput')?.value || '').trim().toLowerCase();
  const models = p.Models || [];

  if (models.length === 0) {
    if (passthroughBanner) passthroughBanner.style.display = 'flex';
    if (countBadge) countBadge.textContent = '0 (未配置)';

    // 默认供应商：未配置清单时承担全能透传；其他供应商：未配置清单则不参与路由
    const isDefaultProvider = (curModelProviderIdx === 0);
    const bannerTitle = $('passthroughBannerTitle');
    const bannerText = $('passthroughBannerText');
    if (isDefaultProvider) {
      if (bannerTitle) bannerTitle.textContent = '穿透模式激活中（默认供应商）';
      if (bannerText) {
        bannerText.textContent = '当前未限制任何模型。未命中路由表的模型请求会原样转发给该供应商，保持全模型兼容。';
      }
    } else {
      if (bannerTitle) bannerTitle.textContent = '未配置模型清单，不参与路由';
      if (bannerText) {
        bannerText.textContent = '该供应商未配置模型清单，因此不参与模型清单合并、冲突判定与按模型路由。请先获取上游模型列表或手动添加。';
      }
    }
    return;
  }

  if (passthroughBanner) passthroughBanner.style.display = 'none';
  if (countBadge) countBadge.textContent = `${models.length} (已限制)`;

  // 顶部汇总条：展示全局模型路由的冲突与遮蔽统计
  if (routePreview.conflicted > 0 || routePreview.unroutable > 0) {
    const summary = document.createElement('div');
    summary.className = 'model-route-summary';
    summary.innerHTML = `模型路由冲突汇总：<span class="accent-count">冲突 ${routePreview.conflicted} 项</span>` +
      `<span class="accent-count">无法路由 ${routePreview.unroutable} 项</span>`;
    container.appendChild(summary);
  }

  const groupsMap = {};
  models.forEach(m => {
    if (search && !m.id.toLowerCase().includes(search) && !(m.name && m.name.toLowerCase().includes(search))) {
      return;
    }
    const g = m.group || '通用 (General)';
    if (!groupsMap[g]) groupsMap[g] = [];
    groupsMap[g].push(m);
  });

  const groupKeys = Object.keys(groupsMap);
  if (groupKeys.length === 0) {
    container.innerHTML = '<div style="padding:1.5rem;text-align:center;color:var(--yorha-fg-dim);font-size:12px;">无匹配的模型条目</div>';
    return;
  }

  groupKeys.forEach(groupName => {
    const list = groupsMap[groupName];
    const groupCard = document.createElement('div');
    groupCard.className = 'model-group-card';

    groupCard.innerHTML = `
      <div class="model-group-header" onclick="this.parentElement.classList.toggle('collapsed')">
        <div class="model-group-title">
          <span class="model-group-arrow">▼</span>
          <span>${escapeHtml(groupName)}</span>
          <span class="badge" style="font-size:10px;">${list.length}</span>
        </div>
        <span style="font-size: 10px; color: var(--yorha-fg-dim); font-family: monospace;">[ 折叠 / 展开 ]</span>
      </div>
      <div class="model-group-body">
        ${list.map(m => {
          const routeInfo = routePreview.byProviderModel[(p.ID || '') + '||' + m.id];
          const providerEnabled = (curModelProviderIdx === 0) || p.Enabled !== false;
          let badge = '';
          if (!providerEnabled) {
            badge = '<span class="model-route-badge disabled">未启用</span>';
          } else if (routeInfo && routeInfo.unroutable) {
            badge = '<span class="model-route-badge unroutable">无法路由</span>';
          } else if (routeInfo && routeInfo.conflicted) {
            badge = '<span class="model-route-badge conflicted">已加前缀</span>';
          }
          let publicIdHtml;
          const prefix = (p.ID || '') + '/';
          if (routeInfo && routeInfo.conflicted && routeInfo.public_id.startsWith(prefix)) {
            publicIdHtml = `<span class="model-public-id"><span class="route-prefix">${escapeHtml(prefix)}</span>${escapeHtml(routeInfo.public_id.slice(prefix.length))}</span>`;
          } else {
            publicIdHtml = `<span class="model-public-id">${escapeHtml((routeInfo && routeInfo.public_id) || m.id)}</span>`;
          }
          const ownerLine = `${p.Name || ''} · 上游 ${m.id}`;
          return `
          <div class="model-item-row">
            <div class="model-item-main">
              <div class="provider-icon-badge" style="width:24px;height:24px;font-size:10px;">
                ${m.protocol === 'anthropic' ? 'AN' : 'AI'}
              </div>
              <div class="model-item-title-box">
                <span class="model-item-name">${escapeHtml(m.name || m.id)} ${badge}</span>
                ${publicIdHtml}
                <span class="model-item-id" style="opacity:0.65;">${escapeHtml(ownerLine)}</span>
              </div>
            </div>
            <div class="model-item-actions">
              <button type="button" class="button btn-model-test" onclick="openLlmTestModal('${escapeHtml(m.id)}', '${m.protocol || 'openai'}')">
                测试
              </button>
              <button type="button" class="button btn-model-remove" onclick="removeModelFromProvider('${escapeHtml(m.id)}')">
                删除
              </button>
            </div>
          </div>
        `;
        }).join('')}
      </div>
    `;

    container.appendChild(groupCard);
  });
}

function filterModelsListUI() {
  renderProviderModelsList();
}

// ── 从当前供应商中删除指定模型 ──
async function removeModelFromProvider(modelId) {
  const p = providersData[curModelProviderIdx];
  if (!p || !p.Models) return;
  p.Models = p.Models.filter(m => m.id !== modelId);
  await persistProvidersConfig(true);
  renderProviderModelsList();
  renderModelProvidersList();
  triggerAlert(`已移除模型 [ ${modelId} ]`);
}

// ── 模态弹窗 1: 从上游抓取可用模型 ──
async function openFetchModelsModal() {
  const modal = $('fetchModelsModal');
  if (!modal) return;
  fetchedSelectedSet.clear();

  modal.style.display = 'flex';
  requestAnimationFrame(() => modal.classList.add('open'));

  await refreshFetchedModels();
}

function closeFetchModelsModal() {
  const modal = $('fetchModelsModal');
  if (modal) {
    modal.classList.remove('open');
    modal.style.display = 'none';
  }
}

async function refreshFetchedModels() {
  const p = providersData[curModelProviderIdx];
  const listEl = $('fetchedModelsList');
  if (!listEl || !p) return;

  listEl.innerHTML = '<div style="padding:1.5rem;text-align:center;color:var(--yorha-fg-dim);font-size:12px;">正在请求上游 GET /v1/models...</div>';
  fetchedModelsPool = [];

  try {
    const res = await fetch('/api/providers/fetch-models', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ base_url: p.BaseURL, api_key: p.APIKey })
    });
    const data = await res.json();
    if (data.error) {
      listEl.innerHTML = `<div style="padding:1.5rem;text-align:center;color:var(--yorha-accent);font-size:12px;">拉取失败: ${escapeHtml(data.error)}</div>`;
      return;
    }
    fetchedModelsPool = data.models || [];
    renderFetchedModelsList();
    triggerAlert(`成功获取到 ${fetchedModelsPool.length} 个上游模型`);
  } catch (err) {
    listEl.innerHTML = `<div style="padding:1.5rem;text-align:center;color:var(--yorha-accent);font-size:12px;">网络请求失败: ${escapeHtml(err.message)}</div>`;
  }
}

function renderFetchedModelsList() {
  const listEl = $('fetchedModelsList');
  if (!listEl) return;
  listEl.innerHTML = '';
  const search = ($('fetchSearchInput')?.value || '').trim().toLowerCase();

  const filtered = fetchedModelsPool.filter(item => {
    if (!search) return true;
    return item.id.toLowerCase().includes(search) || (item.group && item.group.toLowerCase().includes(search));
  });

  if (filtered.length === 0) {
    listEl.innerHTML = '<div style="padding:1.5rem;text-align:center;color:var(--yorha-fg-dim);font-size:12px;">无可用模型或未匹配到关键词</div>';
    return;
  }

  filtered.forEach(item => {
    const row = document.createElement('div');
    const isChecked = fetchedSelectedSet.has(item.id);
    row.className = 'fetched-model-row' + (isChecked ? ' selected' : '');

    row.innerHTML = `
      <label class="yorha-checkbox" onclick="event.stopPropagation()">
        <input type="checkbox" ${isChecked ? 'checked' : ''} onchange="toggleFetchedSelect('${escapeHtml(item.id)}', this.checked)">
      </label>
      <div style="flex:1; min-width:0;">
        <div style="font-weight:bold; font-family:monospace; font-size:12px;">${escapeHtml(item.id)}</div>
        <div style="font-size:10px; color:var(--yorha-fg-dim);">分组: ${escapeHtml(item.group || '通用')}</div>
      </div>
    `;

    row.addEventListener('click', (e) => {
      if (e.target.tagName !== 'INPUT') {
        const cb = row.querySelector('input');
        if (cb) {
          cb.checked = !cb.checked;
          toggleFetchedSelect(item.id, cb.checked);
        }
      }
    });

    listEl.appendChild(row);
  });

  const selCount = $('fetchedSelectedCount');
  if (selCount) selCount.textContent = `已选: ${fetchedSelectedSet.size}`;
}

function filterFetchedModelsUI() {
  renderFetchedModelsList();
}

function toggleFetchedSelect(id, checked) {
  if (checked) fetchedSelectedSet.add(id);
  else fetchedSelectedSet.delete(id);

  const selCount = $('fetchedSelectedCount');
  if (selCount) selCount.textContent = `已选: ${fetchedSelectedSet.size}`;

  const listEl = $('fetchedModelsList');
  if (listEl) {
    const rows = listEl.querySelectorAll('.fetched-model-row');
    rows.forEach(r => {
      const cb = r.querySelector('input');
      if (cb) {
        if (cb.checked) r.classList.add('selected');
        else r.classList.remove('selected');
      }
    });
  }
}

async function confirmImportFetchedModels() {
  const p = providersData[curModelProviderIdx];
  if (!p) return;
  if (!p.Models) p.Models = [];

  let addedCount = 0;
  fetchedSelectedSet.forEach(id => {
    if (!p.Models.some(m => m.id === id)) {
      const match = fetchedModelsPool.find(m => m.id === id);
      p.Models.push({
        id: id,
        name: id,
        group: match ? match.group : '通用 (General)',
        protocol: 'openai'
      });
      addedCount++;
    }
  });

  closeFetchModelsModal();
  await persistProvidersConfig(true);
  renderProviderModelsList();
  renderModelProvidersList();
  triggerAlert(`已导入 ${addedCount} 个模型`);
}

// ── 模态弹窗 2: 手动自定义添加模型 ──
function openCustomModelModal() {
  const modal = $('customModelModal');
  if (!modal) return;
  $('customModelId').value = '';
  $('customModelName').value = '';
  $('customModelGroup').value = '';
  $('customModelProtocol').value = 'openai';
  convertSelectsToYorha('#customModelModal');
  modal.style.display = 'flex';
  requestAnimationFrame(() => modal.classList.add('open'));
}

function closeCustomModelModal() {
  const modal = $('customModelModal');
  if (modal) {
    modal.classList.remove('open');
    modal.style.display = 'none';
  }
}

// ── 基于 ID 从后往前自动解析填写名称与组 ──
function onCustomModelIdInput(val) {
  val = (val || '').trim();
  if (!val) {
    $('customModelName').value = '';
    $('customModelGroup').value = '';
    return;
  }
  const lastSlashIdx = val.lastIndexOf('/');
  if (lastSlashIdx !== -1) {
    const group = val.substring(0, lastSlashIdx).trim();
    const name = val.substring(lastSlashIdx + 1).trim();
    $('customModelGroup').value = group;
    $('customModelName').value = name;
  } else {
    $('customModelName').value = val;
    $('customModelGroup').value = '';
  }
}

async function confirmAddCustomModel() {
  const p = providersData[curModelProviderIdx];
  if (!p) return;
  const id = ($('customModelId').value || '').trim();
  if (!id) {
    triggerAlert('请输入模型 ID');
    return;
  }

  let group = ($('customModelGroup').value || '').trim();
  let name = ($('customModelName').value || '').trim();

  const lastSlashIdx = id.lastIndexOf('/');
  if (lastSlashIdx !== -1) {
    if (!group) {
      group = id.substring(0, lastSlashIdx).trim();
    }
    if (!name) {
      name = id.substring(lastSlashIdx + 1).trim();
    }
  } else {
    if (!group) {
      group = '自定义 (Custom)';
    }
    if (!name) {
      name = id;
    }
  }

  const protocol = $('customModelProtocol').value || 'openai';

  if (!p.Models) p.Models = [];
  const exists = p.Models.find(m => m.id === id);
  if (exists) {
    exists.name = name;
    exists.group = group;
    exists.protocol = protocol;
  } else {
    p.Models.push({ id, name, group, protocol });
  }

  closeCustomModelModal();
  await persistProvidersConfig(true);
  renderProviderModelsList();
  renderModelProvidersList();
  triggerAlert(`已添加模型 [ ${id} ]`);
}

// ── 模态弹窗 3: 添加新供应商 ──
function openAddProviderModal() {
  const modal = $('addProviderModal');
  if (!modal) return;
  $('newProviderPreset').value = 'custom';
  $('newProviderName').value = '';
  $('newProviderBaseUrl').value = '';
  $('newProviderApiKey').value = '';
  const idField = $('newProviderId');
  if (idField) idField.value = '';
  const idHint = $('newProviderIdHint');
  if (idHint) {
    idHint.textContent = '仅允许字母、数字、下划线、短横线，不区分大小写。模型 ID 在多个供应商间重名时，对外模型名显示为 [ID/模型ID]';
    idHint.style.color = '';
  }
  convertSelectsToYorha('#addProviderModal');
  modal.style.display = 'flex';
  requestAnimationFrame(() => modal.classList.add('open'));
}

function closeAddProviderModal() {
  const modal = $('addProviderModal');
  if (modal) {
    modal.classList.remove('open');
    modal.style.display = 'none';
  }
}

function applyProviderPreset(preset) {
  const presets = {
    deepseek: { name: 'DeepSeek 官方', base: 'https://api.deepseek.com' },
    nvidia: { name: 'NVIDIA Integrate', base: 'https://integrate.api.nvidia.com' },
    gemini: { name: 'Google Gemini', base: 'https://generativelanguage.googleapis.com' },
    groq: { name: 'Groq Cloud', base: 'https://api.groq.com' },
    custom: { name: '', base: '' }
  };
  const cfg = presets[preset] || presets.custom;
  if (cfg.name) {
    $('newProviderName').value = cfg.name;
    onNewProviderNameInput(); // 选中预设后同步自动填充 ID
  }
  if (cfg.base) $('newProviderBaseUrl').value = cfg.base;
}

async function confirmAddNewProvider() {
  const name = ($('newProviderName').value || '').trim();
  const base = ($('newProviderBaseUrl').value || '').trim();
  const key = ($('newProviderApiKey').value || '').trim();
  const idRaw = ($('newProviderId').value || '').trim();

  if (!name) {
    triggerAlert('请输入供应商名称');
    return;
  }
  if (!idRaw) {
    triggerAlert('请填写供应商 ID：英文名称会自动填充，非英文名称需手动指定');
    return;
  }
  if (!isValidProviderId(idRaw)) {
    triggerAlert('供应商 ID 仅允许字母、数字、下划线、短横线');
    return;
  }
  if (findProviderIdConflict(idRaw, -1) >= 0) {
    triggerAlert(`供应商 ID [ ${idRaw} ] 已被占用，请更换`);
    return;
  }

  const newP = {
    ID: idRaw,
    Name: name,
    Enabled: true,
    BaseURL: base || 'https://api.example.com',
    AnthropicBaseURL: '',
    APIKey: key,
    Models: []
  };

  providersData.push(newP);
  curModelProviderIdx = providersData.length - 1;

  closeAddProviderModal();
  fillProviderEditor();
  updateDefaultProviderLabel();
  renderModelProvidersList();
  renderCurrentProviderDetail();

  await persistProvidersConfig(true);
  triggerAlert(`供应商 [ ${name} ] 已添加`);
}

// 名称输入：名称为合法英文（字母/数字/下划线/短横线）时自动填充同名 ID，用户可自行修改
function onNewProviderNameInput() {
  const name = ($('newProviderName').value || '').trim();
  const idInput = $('newProviderId');
  if (!idInput) return;
  idInput.value = isValidProviderId(name) ? name : '';
  onNewProviderIdInput();
}

function onNewProviderIdInput() {
  const idRaw = ($('newProviderId').value || '').trim();
  const hint = $('newProviderIdHint');
  if (!hint) return;
  if (!idRaw) {
    hint.textContent = 'ID 不能为空：英文名称会自动填充，非英文名称请手动填写';
    hint.style.color = 'var(--yorha-accent)';
    return;
  }
  if (!isValidProviderId(idRaw)) {
    hint.textContent = 'ID 仅允许字母、数字、下划线、短横线';
    hint.style.color = 'var(--yorha-accent)';
    return;
  }
  const conflictIdx = findProviderIdConflict(idRaw, -1);
  if (conflictIdx >= 0) {
    hint.textContent = `该 ID 已被供应商 [ ${providersData[conflictIdx].Name} ] 使用，请更换`;
    hint.style.color = 'var(--yorha-accent)';
    return;
  }
  hint.textContent = '模型 ID 在多个供应商间重名时，对外模型名显示为 [ID/模型ID]';
  hint.style.color = '';
}

// ── 模态弹窗 4: 最小通用 LLM 可用性测试控制台 ──
function openLlmTestModal(modelId, protocol) {
  currentTestingModel = modelId;
  currentTestingProtocol = protocol;
  const modal = $('llmTestModal');
  if (!modal) return;

  $('testModalModelName').textContent = modelId;
  $('testModalProtocol').textContent = (protocol || 'openai').toUpperCase();
  $('llmTestSerial').textContent = 'STATUS: IDLE';

  const term = $('llmTerminalBody');
  term.innerHTML = `
    <div class="term-line info">&gt; 准备对模型发起连通性可用性检测: ${escapeHtml(modelId)}</div>
    <div class="term-line info">&gt; 协议规范: ${(protocol || 'openai').toUpperCase()}</div>
    <div class="term-line info">&gt; 通用测试提示词: "连接测试，什么都别想，收到回复：收到了"</div>
  `;

  modal.style.display = 'flex';
  requestAnimationFrame(() => modal.classList.add('open'));
}

function closeLlmTestModal() {
  const modal = $('llmTestModal');
  if (modal) {
    modal.classList.remove('open');
    modal.style.display = 'none';
  }
}

// 真实向当前选中的供应商地址发起通用最小化可用性测试请求
async function runMinimalLlmTest() {
  const p = providersData[curModelProviderIdx];
  const btn = $('btnRunLlmTest');
  const term = $('llmTerminalBody');
  const statusSerial = $('llmTestSerial');
  if (!btn || !term || !p) return;

  btn.disabled = true;
  statusSerial.textContent = 'STATUS: IN_FLIGHT';

  const promptText = $('testModalPromptInput')?.value || '连接测试，什么都别想，收到回复：收到了';
  const base = currentTestingProtocol === 'anthropic'
    ? (p.AnthropicBaseURL || p.BaseURL || 'https://api.example.com')
    : (p.BaseURL || 'https://api.example.com');
  const cleanBase = base.replace(/\/+$/, '');
  const predictedURL = currentTestingProtocol === 'anthropic'
    ? `${cleanBase}/v1/messages`
    : (cleanBase.endsWith('/v1') ? `${cleanBase}/chat/completions` : `${cleanBase}/v1/chat/completions`);

  term.innerHTML += `
    <div class="term-line request">&gt; POST ${escapeHtml(predictedURL)}</div>
    <div class="term-line request">&gt; Target Provider: ${escapeHtml(p.Name)} | Model: ${escapeHtml(currentTestingModel)}</div>
    <div class="term-line info">&gt; 正在直接向供应商上游网关发起可用性检测...</div>
  `;
  term.scrollTop = term.scrollHeight;

  try {
    const payload = {
      base_url: p.BaseURL,
      anthropic_base_url: p.AnthropicBaseURL,
      api_key: p.APIKey,
      model: currentTestingModel,
      protocol: currentTestingProtocol,
      prompt: promptText
    };

    const res = await fetch('/api/providers/test-llm', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });

    const data = await res.json();
    const actualURL = data.target_url || predictedURL;

    if (data.ok) {
      term.innerHTML += `
        <div class="term-line success">&gt; HTTP/1.1 ${data.status} ${data.status_text || 'OK'} (耗时: ${data.latency_ms}ms)</div>
        <div class="term-line content">&gt; 回复正文: ${escapeHtml(data.reply || '(无文本)')}</div>
        <div class="term-line success">&gt; [可用性测试通过] 供应商 [ ${escapeHtml(p.Name)} ] 该模型处于就绪状态</div>
      `;
      statusSerial.textContent = `STATUS: ${data.status}_OK`;
    } else {
      term.innerHTML += `
        <div class="term-line danger">&gt; HTTP/1.1 ${data.status || 'ERR'} ${escapeHtml(data.status_text || 'Error')} (耗时: ${data.latency_ms || 0}ms)</div>
        <div class="term-line danger">&gt; 端点: ${escapeHtml(actualURL)}</div>
        <div class="term-line danger">&gt; 错误详情: ${escapeHtml(data.error || data.reply || '上游响应失败')}</div>
      `;
      statusSerial.textContent = data.status ? `STATUS: ${data.status}_ERR` : 'STATUS: NET_ERR';
    }
  } catch (err) {
    term.innerHTML += `
      <div class="term-line danger">&gt; 请求异常: ${escapeHtml(err.message)}</div>
    `;
    statusSerial.textContent = 'STATUS: NETWORK_ERR';
  } finally {
    term.scrollTop = term.scrollHeight;
    btn.disabled = false;
  }
}



