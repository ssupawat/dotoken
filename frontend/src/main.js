import { FetchUsage, SaveSettings, GetSettings, QuitApp, StartPolling } from '../bindings/tokenwatch/tokenwatch.js'
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
  let html = `<div class="section">`;
  html += `<div class="section-label">${p.name}</div>`;

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
}

async function refresh() {
  document.getElementById('updated-text').textContent = 'refreshing...';
  try {
    const data = await FetchUsage();
    render(data);
  } catch (err) {
    document.getElementById('body').innerHTML = `
      <div class="loading">Error: ${err}</div>`;
    document.getElementById('updated-text').textContent = 'error';
  }
}

// ── Settings ──

let isSettingsOpen = false;

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
    } catch (err) {
      console.error(err);
    }
  } else {
    body.style.display = 'block';
    settingsView.style.display = 'none';
    settingsBtn.textContent = 'settings';
    refresh();
  }
}

async function saveSettings() {
  const token = document.getElementById('zai-token').value.trim();
  const session = document.getElementById('claude-session').value.trim();
  try {
    await SaveSettings(token, session);
    toggleSettings(); // go back
  } catch (err) {
    alert(err); // This will show the Go validation error (e.g. "tmux session 'xyz' not found")
  }
}

window.toggleSettings = toggleSettings;
window.saveSettings = saveSettings;
window.quit = quit;

// ── Auto Refresh (every 5 minutes) ──

setInterval(() => {
  if (!isSettingsOpen) {
    refresh();
  }
}, 300000);

function quit() {
  QuitApp();
}

// Listen for real-time usage updates from Go backend
Events.On("usage", (event) => {
  const data = typeof event.data === 'string' ? JSON.parse(event.data) : event.data;
  render(data);
});

// Initial load
refresh();

// Start background polling (5 min intervals)
StartPolling();

