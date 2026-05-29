import { FetchUsage, SaveSettings, GetSettings, QuitApp, SaveProviderOrder } from '../bindings/dotoken/dotoken.js'
import { Events } from '@wailsio/runtime'

function pctLevel(pct) {
  if (pct >= 90) return 'crit';
  if (pct >= 70) return 'warn';
  return '';
}

function formatNumber(n) {
  if (n === undefined || n === null) return '0';
  const s = Math.floor(n).toLocaleString();
  return s;
}

function providerClass(name) {
  const n = name.toLowerCase();
  if (n.includes('claude')) return 'claude';
  if (n.includes('opencode')) return 'opencode';
  if (n.includes('openai')) return 'openai';
  if (n.includes('z.ai')) return 'zai';
  return '';
}

function renderMetric(m, providerClass) {
  // Loading state (pct === -1)
  if (m.pct === -1) {
    return `
      <div class="row ${providerClass}">
        <span class="row-label">${m.label}</span>
        <div class="bar-track"><div class="bar-fill loading-bar"></div></div>
        <span class="row-pct" style="color:var(--dim);font-size:9px">…</span>
      </div>`;
  }

  const level = pctLevel(m.pct);
  const cls = [providerClass, level].filter(Boolean).join(' ');

  if (m.pct > 0) {
    return `
      <div class="row ${cls}">
        <span class="row-label">${m.label}</span>
        <div class="bar-track"><div class="bar-fill" style="width:${Math.min(m.pct, 100)}%"></div></div>
        <span class="row-pct">${Math.round(m.pct)}%</span>
      </div>`;
  }

  // For Claude: show raw token counts instead of percentage
  return `
    <div class="row ${providerClass}">
      <span class="row-label">${m.label}</span>
      <div class="bar-track"></div>
      <span class="row-pct" style="color:var(--dim);font-size:9px">${formatNumber(m.used)}</span>
    </div>`;
}

function renderProvider(p) {
  const cls = providerClass(p.name);
  let html = `<div class="section" draggable="true" data-provider="${p.name}">`;
  html += `<div class="section-header">`;
  html += `<span class="drag-handle" draggable="true">⋮⋮</span>`;
  html += `<div class="section-label">${p.name}</div>`;
  html += `</div>`;

  for (const m of p.metrics) {
    html += renderMetric(m, cls);
  }

  if (p.resetIn) {
    html += `
      <div class="reset-row">
        <span class="reset-label">resets in</span>
        <span class="reset-time">${p.resetIn}</span>
      </div>`;
  }

  html += `</div>`;
  return html;
}

// ── Drag and drop reorder ──

let dragSrc = null;

function handleDragStart(e) {
  dragSrc = this;
  this.classList.add('dragging');
  e.dataTransfer.effectAllowed = 'move';
  e.dataTransfer.setData('text/plain', '');
}

function handleDragOver(e) {
  e.preventDefault();
  e.dataTransfer.dropEffect = 'move';
  const target = this.closest('.section');
  if (target && target !== dragSrc) {
    target.classList.add('drag-over');
  }
}

function handleDragEnd(e) {
  this.classList.remove('dragging');
  document.querySelectorAll('#body .section').forEach(s => s.classList.remove('drag-over'));
  dragSrc = null;
}

function handleDrop(e) {
  e.preventDefault();
  e.stopPropagation();
  const target = this.closest('.section');
  document.querySelectorAll('#body .section').forEach(s => s.classList.remove('drag-over'));
  if (!target || target === dragSrc) return;

  const body = document.getElementById('body');
  const sections = [...body.querySelectorAll('.section')];
  const srcIdx = sections.indexOf(dragSrc);
  const tgtIdx = sections.indexOf(target);
  if (srcIdx === -1 || tgtIdx === -1) return;

  if (srcIdx < tgtIdx) {
    body.insertBefore(dragSrc, target.nextSibling);
  } else {
    body.insertBefore(dragSrc, target);
  }

  // Save new order
  const order = [...body.querySelectorAll('.section')].map(s => s.dataset.provider);
  SaveProviderOrder(order).catch(console.error);
}

// ── Render ──

function render(data) {
  document.getElementById('updated-text').textContent = data.updatedAt;

  if (!data.providers || data.providers.length === 0) {
    document.getElementById('body').innerHTML = `
      <div class="loading">Loading usage data…<br><span style="font-size:10px;color:var(--dim)">first fetch may take a moment</span></div>`;
    return;
  }

  let html = '';
  data.providers.forEach((p, i) => {
    html += renderProvider(p);
    if (i < data.providers.length - 1) {
      html += `<div class="divider"></div>`;
    }
  });

  document.getElementById('body').innerHTML = html;

  // Drag and drop reorder
  const sections = document.querySelectorAll('#body .section');
  sections.forEach(section => {
    section.addEventListener('dragstart', handleDragStart);
    section.addEventListener('dragover', handleDragOver);
    section.addEventListener('drop', handleDrop);
    section.addEventListener('dragend', handleDragEnd);
  });
}

async function refresh() {
  // Render cached data immediately
  if (cachedData) render(cachedData);
  document.getElementById('updated-text').textContent = 'refreshing...';
  // Fetch in background — updates via event when done
  FetchUsage().then(data => {
    cachedData = data;
    render(data);
  }).catch(err => {
    document.getElementById('body').innerHTML = `<div class="loading">Error: ${err}</div>`;
  });
}

// ── Settings ──

let isSettingsOpen = false;
let cachedData = null;

async function toggleSettings() {
  const body = document.getElementById('body');
  const settingsView = document.getElementById('settings-view');
  const settingsBtn = document.getElementById('settings-btn');

  isSettingsOpen = !isSettingsOpen;

  if (isSettingsOpen) {
    body.style.display = 'none';
    settingsView.style.display = 'block';
    settingsBtn.textContent = 'back';

    // Populate current settings
    try {
      const cfg = await GetSettings();
      document.getElementById('zai-token').value = cfg.zaiToken || '';
      document.getElementById('claude-session').value = cfg.claudeSession || '';
      document.getElementById('opencode-cookie').value = cfg.openCodeCookie || '';
    } catch (err) {
      console.error(err);
    }
  } else {
    body.style.display = 'block';
    settingsView.style.display = 'none';
    settingsBtn.textContent = 'settings';
    if (cachedData) render(cachedData);
  }
}

async function saveSettings() {
  const token = document.getElementById('zai-token').value.trim();
  const session = document.getElementById('claude-session').value.trim();
  const cookie = document.getElementById('opencode-cookie').value.trim();
  try {
    const result = await SaveSettings(token, session, cookie);
    // result is the warning string from Go (string, error)
    const warning = result || '';
    if (warning) {
      showWarning(warning);
    } else {
      hideWarning();
    }
    toggleSettings();
  } catch (err) {
    showWarning(err);
  }
}

window.toggleSettings = toggleSettings;
window.saveSettings = saveSettings;
window.quit = quit;

function showWarning(msg) {
  let banner = document.getElementById('warning-banner');
  if (!banner) {
    banner = document.createElement('div');
    banner.id = 'warning-banner';
    document.querySelector('.popover').insertBefore(banner, document.querySelector('.header').nextSibling);
  }
  // Extract the command after "Run: "
  const cmdMatch = msg.match(/Run: (.+)/);
  const cmd = cmdMatch ? cmdMatch[1] : '';
  const displayMsg = cmdMatch ? msg.replace(/Run: .+/, '') : msg;
  banner.innerHTML = `<span>${displayMsg}</span>${cmd ? `<button class="copy-btn" onclick="copyCmd(this, '${cmd.replace(/'/g, "\\'")}')">copy</button>` : ''}`;
  banner.style.display = 'flex';
  banner.style.alignItems = 'center';
  banner.style.gap = '8px';
}

function hideWarning() {
  const banner = document.getElementById('warning-banner');
  if (banner) banner.style.display = 'none';
}

function copyCmd(btn, cmd) {
  navigator.clipboard.writeText(cmd);
  btn.textContent = 'copied';
  setTimeout(() => btn.textContent = 'copy', 1500);
}

function quit() {
  QuitApp();
}

// Listen for real-time usage updates from Go backend
Events.On("usage", (event) => {
  const data = typeof event.data === 'string' ? JSON.parse(event.data) : event.data;
  cachedData = data;
  render(data);
});

// Refresh on first open (debounced)
let lastRefresh = 0;
function refreshOnOpen() {
  const now = Date.now();
  if (now - lastRefresh < 10000) return; // no more than once per 10s
  lastRefresh = now;
  refresh();
}
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && !isSettingsOpen) refreshOnOpen();
});
window.addEventListener('focus', () => {
  if (!isSettingsOpen) refreshOnOpen();
});

// Initial load (returns cache, refresh in background)
refresh();

