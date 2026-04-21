'use strict';

/* ══════════════════════════════════════════
   STATE
══════════════════════════════════════════ */
var deployMode    = 'image';
var liveServices  = [];
var liveMetrics   = [];
var inProgress    = {};
var expandedCards = new Set();
var logReader     = null;
var toastSeq      = 0;
var confirmCb     = null;
var scaleName     = null;

/* ══════════════════════════════════════════
   INIT
══════════════════════════════════════════ */
document.addEventListener('DOMContentLoaded', function () {
  setMode('image');
  loadServices();
  setInterval(loadServices, 5000);
});

var evtSrc = new EventSource('/events');
evtSrc.onmessage = function(e) {
  toast(e.data + ' github webhook triggered', 'info');
  loadServices();
};

/* ══════════════════════════════════════════
   MODE TOGGLE
══════════════════════════════════════════ */
function setMode(m) {
  deployMode = m;
  document.getElementById('btn-img').classList.toggle('active',  m === 'image');
  document.getElementById('btn-repo').classList.toggle('active', m === 'repo');
  document.getElementById('f-src-lbl').textContent            = m === 'image' ? 'Image' : 'GitHub Repo URL';
  document.getElementById('f-src').placeholder                = m === 'image' ? 'nginx:latest' : 'https://github.com/user/repo';
  document.getElementById('f-rep-wrap').style.display         = m === 'image' ? '' : 'none';
  document.getElementById('form-grid').style.gridTemplateColumns =
    m === 'image' ? '160px 1fr 200px 90px' : '160px 1fr 200px';
}

/* ══════════════════════════════════════════
   Refresh
══════════════════════════════════════════ */

function manualRefresh() {
  var btn = document.getElementById('btn-refresh');
  btn.style.transition = 'transform .6s ease';
  setTimeout(function(){ btn.style.transform = 'rotate(0deg)'; }, 600);
  loadServices();
  toast('Refreshed', 'success');
}

/* ══════════════════════════════════════════
   LOAD SERVICES
══════════════════════════════════════════ */
function loadServices() {
  Promise.all([
    fetch('/services').then(function(r){ return r.json(); }).catch(function(){ return []; }),
    fetch('/metrics').then(function(r){ return r.json(); }).catch(function(){ return []; })
  ]).then(function(results) {
    liveServices = results[0] || [];
    liveMetrics  = results[1] || [];
    renderServices();
    renderStats();
  });
}

/* ══════════════════════════════════════════
   DEPLOY
══════════════════════════════════════════ */
function deployService() {
  var name     = document.getElementById('f-name').value.trim();
  var src      = document.getElementById('f-src').value.trim();
  var envRaw   = document.getElementById('f-env').value.trim();
  var replicas = parseInt(document.getElementById('f-rep').value) || 1;

  if (!name)                                          return toast('Service name is required', 'error');
  if (name !== name.toLowerCase() || /\s/.test(name)) return toast('Name must be lowercase, no spaces', 'error');
  if (!src)                                           return toast(deployMode === 'image' ? 'Image is required' : 'Repo URL is required', 'error');
  if (inProgress[name])                               return toast(name + ' is already deploying', 'error');

  var env = {};
  if (envRaw) envRaw.split(',').forEach(function(p) {
    var i = p.indexOf('=');
    if (i > 0) env[p.slice(0,i).trim()] = p.slice(i+1).trim();
  });

  inProgress[name] = { name: name, mode: deployMode, status: deployMode === 'repo' ? 'CLONING' : 'LOADING', logs: '', failed: false, startedAt: Date.now() };
  renderInProgress();

  if (deployMode === 'repo') {
    fetch('/deploy-repo', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: name, repo: src, env: env })
    }).then(function(res) {
      if (!res.ok) return res.text().then(function(t){ failDeploy(name, t); });
      var reader  = res.body.getReader();
      var decoder = new TextDecoder();
      function read() {
        reader.read().then(function(chunk) {
          if (chunk.done) {
            if (inProgress[name] && !inProgress[name].failed) {
              inProgress[name].status = 'READY';
              renderInProgress();
              toast(name + ' deployed', 'success');
              loadServices();
            }
            return;
          }
          var text = decoder.decode(chunk.value);
          if (!inProgress[name]) return;
          inProgress[name].logs += text;
          if (text.includes('STEP:')) {
            var step = text.split('STEP:')[1].trim().split('\n')[0].trim();
            if (step.includes('FAILED')) {
              inProgress[name].status = 'FAILED';
              inProgress[name].failed = true;
              toast(name + ' deployment failed', 'error');
            } else {
              inProgress[name].status = step;
            }
          }
          renderInProgress();
          if (!inProgress[name] || !inProgress[name].failed) read();
        }).catch(function(){ failDeploy(name, 'Stream error'); });
      }
      read();
    }).catch(function(){ failDeploy(name, 'Network error'); });

  } else {
    fetch('/provision', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: name, image: src, replicas: replicas, env: env })
    }).then(function(res) {
      if (!res.ok) return res.text().then(function(t){ failDeploy(name, t); });
      inProgress[name].status = 'DEPLOYING';
      renderInProgress();
      setTimeout(function() {
        if (!inProgress[name]) return;
        inProgress[name].status = 'READY';
        renderInProgress();
        toast(name + ' deployed', 'success');
        loadServices();
      }, 2500);
    }).catch(function(){ failDeploy(name, 'Network error'); });
  }
}

function failDeploy(name, msg) {
  if (!inProgress[name]) return;
  inProgress[name].status = 'FAILED';
  inProgress[name].logs   = msg || '';
  inProgress[name].failed = true;
  renderInProgress();
  toast('Deploy failed: ' + String(msg || '').slice(0, 70), 'error');
}

/* ══════════════════════════════════════════
   SERVICE ACTIONS  — ALL USE CUSTOM MODALS
══════════════════════════════════════════ */
function deleteService(name) {
  showConfirm(
    'Delete Service',
    'Are you sure you want to delete "' + name + '"? This cannot be undone.',
    true,
    function() {
      inProgress[name] = { name: name, mode: 'image', status: 'DELETING', logs: '', failed: false, deleting: true, startedAt: Date.now() };
      renderInProgress();
      renderServices();
      fetch('/delete?name=' + encodeURIComponent(name)).then(function(res) {
        delete inProgress[name];
        renderInProgress();
        loadServices();
        toast(res.ok ? name + ' deleted' : 'Failed to delete ' + name, res.ok ? 'success' : 'error');
      });
    }
  );
}

function rollbackService(name) {
  showConfirm(
    'Rollback Service',
    'Roll back "' + name + '" to the previous version?',
    false,
    function() {
      fetch('/rollback?name=' + encodeURIComponent(name)).then(function(res) {
        toast(res.ok ? name + ' rolled back' : 'Rollback failed', res.ok ? 'success' : 'error');
        loadServices();
      });
    }
  );
}

function restartService(name) {
  toast('Restarting ' + name + '…', 'info');
  fetch('/restart?name=' + encodeURIComponent(name)).then(function(res) {
    toast(res.ok ? name + ' restarted' : 'Failed to restart ' + name, res.ok ? 'success' : 'error');
    loadServices();
  });
}

function scaleService(name) {
  scaleName = name;
  document.getElementById('scale-svc-name').textContent = name;
  document.getElementById('scale-val').value            = '1';
  document.getElementById('scale-overlay').classList.add('open');
}

function doScale() {
  var n = parseInt(document.getElementById('scale-val').value);
  if (isNaN(n) || n < 1 || n > 5) return toast('Replicas must be 1–5', 'error');
  var name = scaleName;
  cancelScale();
  fetch('/service', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: name, replicas: n })
  }).then(function() {
    toast(name + ' scaled to ' + n + ' replicas', 'success');
    loadServices();
  });
}

function cancelScale() {
  document.getElementById('scale-overlay').classList.remove('open');
  scaleName = null;
}

function deletePod(pod) {
  showConfirm(
    'Delete Pod',
    'Delete pod "' + pod + '"?',
    true,
    function() {
      fetch('/delete-pod?pod=' + encodeURIComponent(pod)).then(function() {
        loadServices();
      });
    }
  );
}

function execPod(pod) {
  fetch('/exec?pod=' + encodeURIComponent(pod));
  toast('Opening terminal for ' + pod, 'info');
}

/* ══════════════════════════════════════════
   HEALTH
══════════════════════════════════════════ */
function getHealth(s) {
  var st = s.pods.map(function(p){ return p.status; });
  if (st.indexOf('Pending') !== -1 || st.indexOf('ContainerCreating') !== -1)
    return { text:'Deploying', color:'var(--blue)',  dot:'var(--blue-l)'  };
  if (st.some(function(x){ return x.indexOf('Error') !== -1 || x.indexOf('CrashLoopBackOff') !== -1 || x.indexOf('ImagePullBackOff') !== -1; }))
    return { text:'Failed',   color:'var(--red)',   dot:'var(--red-l)'   };
  if (s.running === s.replicas && s.replicas > 0)
    return { text:'Healthy',  color:'var(--green)', dot:'var(--green-l)' };
  if (s.running > 0)
    return { text:'Degraded', color:'var(--amber)', dot:'var(--amber-l)' };
  return   { text:'Deploying', color:'var(--blue)', dot:'var(--blue-l)'  };
}

/* ══════════════════════════════════════════
   RENDER STATS
══════════════════════════════════════════ */
function renderStats() {
  var healthy = liveServices.filter(function(s){ return getHealth(s).text === 'Healthy'; }).length;
  var failed  = liveServices.filter(function(s){ return getHealth(s).text === 'Failed';  }).length;
  document.getElementById('stat-healthy').textContent = healthy + ' healthy';
  document.getElementById('stat-total').textContent   = liveServices.length + ' total';
  var fp = document.getElementById('pill-failed');
  fp.style.display = failed > 0 ? '' : 'none';
  document.getElementById('stat-failed').textContent  = failed + ' failed';
}

/* ══════════════════════════════════════════
   RENDER IN-PROGRESS
══════════════════════════════════════════ */
function renderInProgress() {
  var entries = Object.values(inProgress);
  var section = document.getElementById('ip-section');
  var list    = document.getElementById('ip-list');
  if (!entries.length) { section.style.display = 'none'; list.innerHTML = ''; return; }
  section.style.display = '';
  document.getElementById('ip-count').textContent = entries.length;
  list.innerHTML = entries.map(buildIPCard).join('');
}

function buildIPCard(d) {
  var STEPS = d.mode === 'repo' ? ['CLONING','BUILDING','DEPLOYING','READY'] : ['LOADING','DEPLOYING','READY'];
  var isDeleting = d.deleting, isFailed = d.failed, isReady = d.status === 'READY';
  var statusColor = isFailed ? 'var(--red)' : isReady ? 'var(--green)' : 'var(--blue)';
  var statusLabel = isDeleting ? 'Deleting…' : isFailed ? 'Failed' : isReady ? 'Ready' : d.status;
  var dotColor    = isFailed ? 'var(--red-l)' : isReady ? 'var(--green-l)' : 'var(--blue-l)';

  var dotHTML = (isReady || isFailed)
    ? '<span class="dot" style="background:' + dotColor + ';box-shadow:0 0 5px ' + dotColor + '"></span>'
    : '<span class="dot-pulse"><span class="dot-pulse-ring" style="background:' + dotColor + '"></span><span class="dot-pulse-core" style="background:' + dotColor + '"></span></span>';

  var dismissBtn = (isReady || isFailed)
    ? '<button class="ip-dismiss" onclick="dismissIP(\'' + esc(d.name) + '\')">Dismiss</button>' : '';

  var currentIdx = isReady ? STEPS.length : STEPS.findIndex(function(s){ return d.status === s || d.status.indexOf(s) !== -1; });
  var stepsHTML = '';
  if (!isDeleting) {
    stepsHTML = '<div class="step-bar">';
    STEPS.forEach(function(step, i) {
      var done   = isReady || i < currentIdx;
      var active = !isFailed && !isReady && i === currentIdx;
      var fail   = isFailed && i === currentIdx;
      var col    = fail ? 'var(--red)' : done ? 'var(--green)' : active ? 'var(--blue)' : 'var(--border2)';
      var txtCol = fail ? 'var(--red-l)' : done ? 'var(--green-l)' : active ? 'var(--blue-l)' : 'var(--dim)';
      stepsHTML += '<div class="step-item">'
        + '<div class="step-dot" style="background:' + col + ';' + (active || fail ? 'box-shadow:0 0 8px ' + col : '') + '"></div>'
        + '<span class="step-lbl" style="color:' + txtCol + ';font-weight:' + (active ? 600 : 400) + '">' + step + '</span>'
        + '</div>';
      if (i < STEPS.length - 1) stepsHTML += '<div class="step-line' + (done ? ' done' : '') + '"></div>';
    });
    stepsHTML += '</div>';
  }

  var errHTML = isFailed && d.logs ? '<div class="err-log">' + esc(d.logs.slice(-400)) + '</div>' : '';

  return '<div class="ip-card" style="background:var(--surf);border:1px solid ' + statusColor + '30;border-left:3px solid ' + statusColor + '">'
    + '<div class="ip-top">'
    + '<div class="ip-name-row">' + dotHTML + '<span class="ip-name">' + esc(d.name) + '</span>'
    + '<span class="src-badge src-' + d.mode + '">' + d.mode + '</span></div>'
    + '<div style="display:flex;align-items:center;gap:9px"><span class="ip-status-label" style="color:' + statusColor + '">' + statusLabel + '</span>' + dismissBtn + '</div>'
    + '</div>' + stepsHTML + errHTML + '</div>';
}

function dismissIP(name) {
  delete inProgress[name];
  renderInProgress();
}

/* ══════════════════════════════════════════
   RENDER SERVICES
══════════════════════════════════════════ */
function renderServices() {
  var displayed = liveServices.filter(function(s) {
    var d = inProgress[s.name];
    return !d || (d.status === 'READY' && !d.deleting);
  });
  document.getElementById('svc-count').textContent = displayed.length;
  var list = document.getElementById('svc-list');
  if (!displayed.length) {
    list.innerHTML = '<div id="empty-state">'
      + '<div style="font-size:36px;margin-bottom:12px;opacity:.5">⬡</div>'
      + '<div style="font-size:14px;font-weight:600;color:var(--muted);margin-bottom:4px">No services deployed yet</div>'
      + '<div style="font-size:12px;color:var(--dim)">Deploy your first service above to get started</div>'
      + '</div>';
    return;
  }
  list.innerHTML = displayed.slice().sort(function(a,b){ return a.name.localeCompare(b.name); }).map(buildServiceCard).join('');
}

function buildServiceCard(s) {
  var health   = getHealth(s);
  var isOpen   = expandedCards.has(s.name);
  var totalCPU = 0, totalMem = 0;
  s.pods.forEach(function(p) {
    var m = liveMetrics.find(function(x){ return x.pod === p.pod; });
    if (m) { totalCPU += parseInt(m.cpu) || 0; totalMem += parseInt(m.memory) || 0; }
  });

  var podsHTML = s.pods.map(function(p) {
    var m  = liveMetrics.find(function(x){ return x.pod === p.pod; }) || { cpu:'-', memory:'-' };
    var ok = p.status === 'Running';
    return '<div class="pod-row">'
      + '<span class="dot" style="background:' + (ok ? 'var(--green-l)' : 'var(--red-l)') + ';box-shadow:0 0 5px ' + (ok ? 'var(--green-l)' : 'var(--red-l)') + '"></span>'
      + '<span class="pod-name">' + esc(p.pod) + '</span>'
      + '<span class="pod-status" style="color:' + (ok ? 'var(--green)' : 'var(--red-l)') + '">' + p.status + '</span>'
      + '<span class="pod-metric">' + m.cpu + 'm</span>'
      + '<span class="pod-metric">' + m.memory + 'Mi</span>'
      + '<button class="btn btn-ghost btn-sm" onclick="showLogs(\'' + esc(p.pod) + '\')">Logs</button>'
      + '<button class="btn btn-ghost btn-sm" onclick="execPod(\'' + esc(p.pod) + '\')">⌘ Exec</button>'
      + '<button class="btn btn-ghost btn-sm" onclick="deletePod(\'' + esc(p.pod) + '\')" style="color:var(--red-l)">✕ Del</button>'
      + '</div>';
  }).join('');

  return '<div class="svc-card">'
    + '<div class="svc-top" onclick="toggleExpand(\'' + esc(s.name) + '\')">'
    + '<span class="dot" style="background:' + health.dot + ';box-shadow:0 0 5px ' + health.dot + '"></span>'
    + '<div class="svc-info">'
    + '<div class="svc-name-row"><span class="svc-name">' + esc(s.name) + '</span>'
    + '<span class="src-badge src-' + (s.source === 'repo' ? 'repo' : 'image') + '">' + esc(s.source) + '</span></div>'
    + '<div class="svc-sub">' + s.running + '/' + s.replicas + ' pods running</div>'
    + '</div>'
    + '<div class="svc-metrics">'
    + '<div class="metric-col"><div class="metric-label">CPU</div><div class="metric-val">' + totalCPU + 'm</div></div>'
    + '<div class="metric-col"><div class="metric-label">MEM</div><div class="metric-val">' + totalMem + 'Mi</div></div>'
    + '<div class="health-pill" style="background:' + health.color + '18;border:1px solid ' + health.color + '30">'
    + '<span style="color:' + health.color + ';font-size:12px;font-weight:700">' + health.text + '</span></div>'
    + '</div>'
    + '<span class="chevron' + (isOpen ? ' open' : '') + '">▾</span>'
    + '</div>'
    + '<div class="svc-body' + (isOpen ? ' open' : '') + '">'
    + '<div class="pods-list">' + podsHTML + '</div>'
    + '<div class="actions">'
    + '<button class="btn btn-blue"  onclick="window.open(\'http://' + esc(s.name) + '.127.0.0.1.nip.io\')">↗ Open</button>'
    + '<button class="btn btn-ghost" onclick="restartService(\'' + esc(s.name) + '\')">↺ Restart</button>'
    + '<button class="btn btn-ghost" onclick="scaleService(\''   + esc(s.name) + '\')">⇡ Scale</button>'
    + '<button class="btn btn-ghost" onclick="rollbackService(\'' + esc(s.name) + '\')">⏪ Rollback</button>'
    + '<button class="btn btn-ghost" onclick="showHistory(\''    + esc(s.name) + '\')">⧖ History</button>'
    + '<button class="btn btn-ghost" onclick="diagnoseService(\'' + esc(s.name) + '\')" style="color:var(--amber-l);border-color:#f59e0b40;background:#f59e0b18">🩺 Diagnose</button>'
    + '<button class="btn btn-red"   onclick="deleteService(\''  + esc(s.name) + '\')">✕ Delete</button>'
    + '</div>'
    + '</div>'
    + '</div>';
}

function toggleExpand(name) {
  if (expandedCards.has(name)) expandedCards.delete(name);
  else expandedCards.add(name);
  renderServices();
}

/* ══════════════════════════════════════════
   LOGS
══════════════════════════════════════════ */
function showLogs(pod) {
  document.getElementById('logs-pod').textContent = pod;
  document.getElementById('logs-pre').textContent = 'Connecting…\n';
  document.getElementById('logs-overlay').classList.add('open');
  if (logReader) { logReader.cancel(); logReader = null; }
  fetch('/logs?pod=' + encodeURIComponent(pod)).then(function(res) {
    logReader       = res.body.getReader();
    var dec         = new TextDecoder();
    var pre         = document.getElementById('logs-pre');
    var body        = document.getElementById('logs-body');
    pre.textContent = '';
    function read() {
      logReader.read().then(function(chunk) {
        if (chunk.done) return;
        pre.textContent += dec.decode(chunk.value);
        body.scrollTop   = body.scrollHeight;
        read();
      }).catch(function(){});
    }
    read();
  }).catch(function() {
    document.getElementById('logs-pre').textContent = 'Failed to connect.';
  });
}

function closeLogs() {
  document.getElementById('logs-overlay').classList.remove('open');
  if (logReader) { logReader.cancel(); logReader = null; }
}

/* ══════════════════════════════════════════
   HISTORY
══════════════════════════════════════════ */
function showHistory(name) {
  document.getElementById('hist-svc').textContent = name;
  document.getElementById('hist-body').innerHTML  = '<div style="color:var(--dim);text-align:center;padding:40px 0">Loading…</div>';
  document.getElementById('hist-overlay').classList.add('open');
  fetch('/history?name=' + encodeURIComponent(name)).then(function(r){ return r.json(); }).then(function(data) {
    var body = document.getElementById('hist-body');
    if (!data || !data.length) { body.innerHTML = '<div style="color:var(--dim);text-align:center;padding:40px 0">No history available</div>'; return; }
    body.innerHTML = data.map(function(h) {
      return '<div class="hist-entry">'
        + '<div class="hist-top"><span style="color:var(--purple-l);font-size:12px;font-weight:600">Rev #' + h.revision + '</span><span style="color:var(--dim);font-size:11px">' + esc(h.updated||'') + '</span></div>'
        + '<div style="color:var(--green);font-size:12px;font-weight:500;margin-bottom:4px">' + esc(h.status||'') + '</div>'
        + '<div style="color:var(--muted);font-size:12px">' + esc(h.description||'') + '</div>'
        + '</div>';
    }).join('');
  }).catch(function() {
    document.getElementById('hist-body').innerHTML = '<div style="color:var(--red-l);text-align:center;padding:40px 0">Failed to load history</div>';
  });
}

function closeHistory() {
  document.getElementById('hist-overlay').classList.remove('open');
}

/* ══════════════════════════════════════════
   CONFIRM MODAL  — replaces browser confirm()
══════════════════════════════════════════ */
function showConfirm(title, msg, danger, cb) {
  confirmCb = cb;
  document.getElementById('confirm-title').textContent = title;
  document.getElementById('confirm-msg').textContent   = msg;
  var ok = document.getElementById('confirm-ok');
  if (danger) {
    ok.style.cssText = 'background:#ef444420;border:1px solid #ef444440;border-radius:7px;color:var(--red-l);padding:7px 18px;font-size:13px;font-weight:600;cursor:pointer;font-family:Inter,sans-serif';
    ok.textContent   = 'Delete';
  } else {
    ok.style.cssText = 'background:linear-gradient(135deg,#2563eb,#7c3aed);border:none;border-radius:7px;color:#fff;padding:7px 18px;font-size:13px;font-weight:600;cursor:pointer;font-family:Inter,sans-serif';
    ok.textContent   = 'Confirm';
  }
  document.getElementById('confirm-overlay').classList.add('open');
}

function doConfirm() {
  var cb = confirmCb;
  cancelConfirm();
  if (cb) cb();
}

function cancelConfirm() {
  document.getElementById('confirm-overlay').classList.remove('open');
  confirmCb = null;
}

/* ══════════════════════════════════════════
   TOAST
══════════════════════════════════════════ */
function toast(msg, type) {
  type = type || 'info';
  var id     = ++toastSeq;
  var colors = { success:'var(--green)', error:'var(--red)', info:'var(--blue)' };
  var icons  = { success:'✓', error:'✕', info:'ℹ' };
  var el = document.createElement('div');
  el.className = 'toast';
  el.id = 't-' + id;
  el.style.borderLeft = '3px solid ' + colors[type];
  el.style.border     = '1px solid ' + (colors[type]).replace(')', '') + '30)';
  el.style.borderLeft = '3px solid ' + colors[type];
  el.innerHTML = '<span style="color:' + colors[type] + ';font-size:14px;flex-shrink:0">' + icons[type] + '</span>'
    + '<span style="flex:1;line-height:1.4;color:var(--text)">' + esc(msg) + '</span>'
    + '<button class="toast-close" onclick="removeToast(' + id + ')">×</button>';
  document.getElementById('toast-wrap').appendChild(el);
  setTimeout(function(){ removeToast(id); }, 4000);
}

function removeToast(id) {
  var el = document.getElementById('t-' + id);
  if (el) el.remove();
}

/* ══════════════════════════════════════════
   UTILITY
══════════════════════════════════════════ */
function esc(s) {
  return String(s)
    .replace(/&/g,  '&amp;')
    .replace(/</g,  '&lt;')
    .replace(/>/g,  '&gt;')
    .replace(/"/g,  '&quot;')
    .replace(/'/g,  '&#39;');
}

/* ══════════════════════════════════════════
   DIAGNOSE SERVICE
══════════════════════════════════════════ */

/* ══════════════════════════════════════════
   DIAGNOSE
══════════════════════════════════════════ */
function diagnoseService(name) {
  document.getElementById('diag-svc').textContent   = name;
  document.getElementById('diag-body').innerHTML    = '<div style="color:var(--dim);text-align:center;padding:30px 0">🔎 Diagnosing…</div>';
  document.getElementById('diag-overlay').classList.add('open');

  fetch('/diagnose?name=' + encodeURIComponent(name))
    .then(function(r){ if (!r.ok) throw new Error(r.statusText); return r.json(); })
    .then(function(data) {
      var statusLow = (data.status || '').toLowerCase();
      var color =
        statusLow.indexOf('running') !== -1 ? 'var(--green)' :
        statusLow.indexOf('pending') !== -1 ? 'var(--blue)'  :
        'var(--red)';

      document.getElementById('diag-body').innerHTML =
          '<div style="background:var(--surf2);border:1px solid var(--border);border-radius:8px;padding:12px 14px;margin-bottom:12px">'
        +   '<div style="font-size:10px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin-bottom:6px">Status</div>'
        +   '<div style="display:flex;align-items:center;gap:8px"><span class="dot" style="background:' + color + ';box-shadow:0 0 5px ' + color + '"></span>'
        +   '<span style="color:' + color + ';font-weight:600;font-size:13px">' + esc(data.status || 'Unknown') + '</span></div>'
        +   (data.pod ? '<div style="font-family:\'JetBrains Mono\',monospace;font-size:11px;color:var(--dim);margin-top:6px">' + esc(data.pod) + '</div>' : '')
        + '</div>'
        + '<div style="background:#ef444414;border:1px solid #ef444430;border-left:3px solid var(--red);border-radius:8px;padding:12px 14px;margin-bottom:10px">'
        +   '<div style="font-size:10px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:var(--red-l);margin-bottom:5px">Root Cause</div>'
        +   '<div style="font-size:13px;color:var(--text);line-height:1.5">' + esc(data.cause || '—') + '</div>'
        + '</div>'
        + '<div style="background:#10b98114;border:1px solid #10b98130;border-left:3px solid var(--green);border-radius:8px;padding:12px 14px">'
        +   '<div style="font-size:10px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:var(--green-l);margin-bottom:5px">Suggested Fix</div>'
        +   '<div style="font-size:13px;color:var(--text);line-height:1.5">' + esc(data.suggestion || '—') + '</div>'
        + '</div>';
    })
    .catch(function(err) {
      document.getElementById('diag-body').innerHTML =
        '<div style="color:var(--red-l);text-align:center;padding:30px 0">Failed to diagnose: ' + esc(err.message || 'unknown') + '</div>';
    });
}

function closeDiagnose() {
  document.getElementById('diag-overlay').classList.remove('open');
}
