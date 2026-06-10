'use strict';

// ── Helpers ───────────────────────────────────────────────────────────────────
function parseGitURL(url) {
  // Handles https://github.com/owner/repo.git  and  git@github.com:owner/repo.git
  const https = url.match(/https?:\/\/[^/]+\/([^/]+)\/([^/]+?)(?:\.git)?$/);
  if (https) return { owner: https[1], name: https[2] };
  const ssh = url.match(/git@[^:]+:([^/]+)\/([^/]+?)(?:\.git)?$/);
  if (ssh)   return { owner: ssh[1],   name: ssh[2] };
  return null;
}

// ── Session / auth state ──────────────────────────────────────────────────────
let _currentUser = null; // { id, username, role, must_change_pw }

// Live SSE log stream handle — declared up top because the router (which runs
// at boot, before the runs-tab section) calls closeLogStream().
let logStream = null;

async function loadMe() {
  const resp = await fetch('/api/me');
  if (resp.status === 401) {
    window.location.href = '/login.html';
    return;
  }
  if (resp.ok) {
    _currentUser = await resp.json();
    renderUserBar();
    if (_currentUser.must_change_pw) showChangePWBanner();
  }
}

function renderUserBar() {
  if (!_currentUser) return;
  const label = _currentUser.username + ' (' + _currentUser.role + ')';
  const el = document.getElementById('user-bar');
  if (el) el.textContent = label;
  const mu = document.getElementById('more-user');
  if (mu) mu.textContent = 'Signed in as ' + label;
  // Hide admin-only UI elements for viewers
  if (_currentUser.role !== 'admin') {
    document.getElementById('admin-users-section')?.classList.add('viewer-hidden');
    document.getElementById('add-repo-btn')?.classList.add('viewer-hidden');
  }
}

function showChangePWBanner() {
  const b = document.getElementById('change-pw-banner');
  if (b) b.classList.remove('hidden');
}

async function doLogout() {
  await fetch('/api/logout', { method: 'POST' });
  window.location.href = '/login.html';
}

// ── Rate-limit state ──────────────────────────────────────────────────────────
const RL = { remaining: null, limit: null, reset: null };

function updateRLFromResponse(resp) {
  const rem   = resp.headers.get('X-RateLimit-Remaining');
  const lim   = resp.headers.get('X-RateLimit-Limit');
  const reset = resp.headers.get('X-RateLimit-Reset');
  if (rem   !== null) RL.remaining = parseInt(rem,   10);
  if (lim   !== null) RL.limit     = parseInt(lim,   10);
  if (reset !== null) RL.reset     = parseInt(reset, 10);
  renderRLBanner();
  renderRLNav();
  renderRLInfo();
}

function renderRLBanner() {
  const banner = document.getElementById('rl-banner');
  if (RL.remaining === null) { banner.classList.add('hidden'); return; }
  if (RL.remaining < 20) {
    banner.className = 'rl-banner rl-red';
    banner.textContent = `Rate limit: ${RL.remaining} requests remaining this hour`;
  } else if (RL.remaining < 100) {
    banner.className = 'rl-banner rl-yellow';
    banner.textContent = `Rate limit: ${RL.remaining} requests remaining this hour`;
  } else {
    banner.classList.add('hidden');
    return;
  }
  banner.classList.remove('hidden');
}

function renderRLNav() {
  const el = document.getElementById('rl-nav-badge');
  if (RL.remaining === null || RL.remaining >= 100) { el.classList.add('hidden'); return; }
  el.className = RL.remaining < 20 ? 'rl-nav rl-red' : 'rl-nav rl-yellow';
  el.textContent = `${RL.remaining} req left`;
  el.classList.remove('hidden');
}

function renderRLInfo() {
  const box = document.getElementById('rl-info');
  if (!box) return;
  if (RL.remaining === null) { box.classList.add('hidden'); return; }
  box.classList.remove('hidden');
  document.getElementById('rl-remaining').textContent = RL.remaining;
  document.getElementById('rl-limit').textContent     = RL.limit ?? '—';
  document.getElementById('rl-reset').textContent     = RL.reset
    ? new Date(RL.reset * 1000).toLocaleTimeString()
    : '—';
}

// ── Fetch wrapper ─────────────────────────────────────────────────────────────
async function apiFetch(url, opts) {
  const resp = await fetch(url, opts);
  if (resp.status === 401) {
    window.location.href = '/login.html';
    return resp;
  }
  updateRLFromResponse(resp);
  return resp;
}

// ── Safe DOM helpers ──────────────────────────────────────────────────────────
function makeTd(text) {
  const td = document.createElement('td');
  td.textContent = text ?? '—';
  return td;
}

function makeTdHTML(html) {
  const td = document.createElement('td');
  td.innerHTML = html;
  return td;
}

// ── Status helpers ────────────────────────────────────────────────────────────
function badge(status) {
  const safe = String(status).replace(/[^a-z_]/g, '');
  return `<span class="badge badge-${safe}">${safe}</span>`;
}

const STATUS_ICONS = {
  pending:  '&#x25CB;',  // hollow circle
  running:  '&#x21BB;',  // spinning arrows (static)
  success:  '&#x2714;',  // checkmark
  failure:  '&#x2716;',  // X
  skipped:  '&#x23E9;',  // fast-forward
  cancelled:'&#x23F9;',  // stop
};

function statusIcon(status) {
  const icon = STATUS_ICONS[status] ?? '&#x25CB;';
  const cls  = `step-icon step-icon-${String(status).replace(/[^a-z_]/g, '')}`;
  return `<span class="${cls}">${icon}</span>`;
}

// ── Time helpers ──────────────────────────────────────────────────────────────
function fmtTime(ts) {
  if (!ts) return '—';
  return new Date(ts * 1000).toLocaleString();
}

function fmtDuration(startedAt, finishedAt) {
  if (!startedAt) return '—';
  const end = finishedAt ?? Math.floor(Date.now() / 1000);
  const s = end - startedAt;
  if (s < 60) return s + 's';
  return Math.floor(s / 60) + 'm ' + (s % 60) + 's';
}

function fmtWait(createdAt) {
  const s = Math.floor(Date.now() / 1000) - createdAt;
  if (s < 60) return s + 's';
  return Math.floor(s / 60) + 'm ' + (s % 60) + 's';
}

function shortSHA(sha) {
  return sha ? sha.slice(0, 7) : '—';
}

// ── Tab routing ───────────────────────────────────────────────────────────────
const TAB_LOADERS = {
  runs:     () => loadRuns(),
  queue:    () => loadQueue(),
  repos:    () => { showReposList(); loadRepos(); },
  settings: () => loadSettings(),
  users:    () => { loadUsers(); loadAPIKeys(); },
};

// URL path for each tab. Runs is the home page; the rest are plain paths.
const TAB_PATHS = { runs: '/', queue: '/queue', repos: '/repos', settings: '/settings', users: '/users' };

document.querySelectorAll('.tab-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    pushURL(TAB_PATHS[btn.dataset.tab] ?? '/');
    switchToTab(btn.dataset.tab);
    TAB_LOADERS[btn.dataset.tab]?.();
  });
});

function switchToTab(name) {
  const btn = document.querySelector(`.tab-btn[data-tab="${name}"]`);
  if (!btn) return;
  document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  btn.classList.add('active');
  document.getElementById('tab-' + name).classList.add('active');
  // Mirror state onto the mobile bottom nav ("More" owns settings + users).
  document.querySelectorAll('.bnav-btn').forEach(b => {
    const active = b.id === 'bnav-more'
      ? (name === 'settings' || name === 'users')
      : b.dataset.tab === name;
    b.classList.toggle('active', active);
  });
}

// ── Mobile bottom nav + "More" sheet ──────────────────────────────────────────
function openMoreSheet() {
  document.getElementById('more-sheet')?.classList.remove('hidden');
  document.getElementById('more-backdrop')?.classList.remove('hidden');
}
function closeMoreSheet() {
  document.getElementById('more-sheet')?.classList.add('hidden');
  document.getElementById('more-backdrop')?.classList.add('hidden');
}

document.querySelectorAll('.bnav-btn[data-tab], .more-item[data-tab]').forEach(btn => {
  btn.addEventListener('click', () => {
    closeMoreSheet();
    pushURL(TAB_PATHS[btn.dataset.tab] ?? '/');
    switchToTab(btn.dataset.tab);
    TAB_LOADERS[btn.dataset.tab]?.();
  });
});
document.getElementById('bnav-more')?.addEventListener('click', openMoreSheet);
document.getElementById('more-backdrop')?.addEventListener('click', closeMoreSheet);
document.getElementById('more-logout')?.addEventListener('click', doLogout);

// ── URL router ────────────────────────────────────────────────────────────────
// Paths imitate GitHub Actions so links read naturally and survive refresh:
//   /                                      runs list (home)
//   /queue /repos /settings /users         tabs
//   /runs/{id}                             run detail (when owner unknown)
//   /{owner}/{repo}                        repo detail
//   /{owner}/{repo}/actions                repo detail (workflows)
//   /{owner}/{repo}/actions/runs/{id}      run detail
//   /{owner}/{repo}/actions/workflows/{f}  runs filtered by repo+workflow
// The server serves index.html for any unknown path (SPA fallback).

function pushURL(path) {
  if (window.location.pathname !== path) history.pushState({}, '', path);
}

// Repo cache: maps repo_id → repo so we can build /{owner}/{repo} URLs from runs.
let _repos = null;
async function getRepos() {
  if (_repos) return _repos;
  const r = await apiFetch('/api/repos');
  _repos = r.ok ? await r.json() : [];
  return _repos;
}
function repoByID(id) { return (_repos || []).find(x => x.id === id); }

function runURL(run) {
  const repo = repoByID(run.repo_id);
  return repo
    ? `/${repo.owner}/${repo.name}/actions/runs/${run.id}`
    : `/runs/${run.id}`;
}

function workflowNameFromFile(file) {
  return file.replace(/\.ya?ml$/i, '');
}

async function openRunById(id, sel) {
  const r = await apiFetch(`/api/runs/${id}`);
  if (!r.ok) { loadRuns(); return; }
  openRun(await r.json(), sel);
}

async function openRepoByOwnerName(owner, name, view) {
  const repos = await getRepos();
  const repo = repos.find(x => x.owner === owner && x.name === name);
  if (repo) { openRepo(repo, view); } else { showReposList(); loadRepos(); }
}

async function render(path) {
  closeLogStream();
  await getRepos(); // warm the cache so URLs and gh-compat calls resolve
  const seg = path.split('/').filter(Boolean);

  if (seg.length === 0) { switchToTab('runs'); loadRuns(); return; }

  const [owner, repo, c, d, e] = seg;
  if (seg.length === 1) {
    if (owner === 'runs') { switchToTab('runs'); loadRuns(); return; }
    if (TAB_PATHS[owner]) { switchToTab(owner); TAB_LOADERS[owner]?.(); return; }
  }
  if (owner === 'runs' && seg.length === 2) { switchToTab('runs'); openRunById(repo); return; }

  if (seg.length >= 2) {
    if (seg.length === 2 || (seg.length === 3 && c === 'actions')) {
      switchToTab('repos'); openRepoByOwnerName(owner, repo); return;
    }
    // /{owner}/{repo}/settings/secrets — secrets & variables pane
    if (c === 'settings' && seg.length >= 3) {
      switchToTab('repos');
      openRepoByOwnerName(owner, repo, { pane: 'secrets' });
      return;
    }
    // /{owner}/{repo}/tree|blob/{branch}/{path...} — file browser deep links
    if ((c === 'tree' || c === 'blob') && seg.length >= 4) {
      switchToTab('repos');
      openRepoByOwnerName(owner, repo, { pane: 'files', mode: c, path: seg.slice(4).join('/') });
      return;
    }
    // /{owner}/{repo}/actions/runs/{id}[/job/{jobId}] — selected job survives refresh
    if (c === 'actions' && d === 'runs' && e) {
      const sel = seg[5] === 'job' && seg[6]
        ? { jobID: parseInt(seg[6], 10), step: stepFromQuery() }
        : { step: stepFromQuery() };
      switchToTab('runs'); openRunById(e, sel); return;
    }
    if (c === 'actions' && d === 'workflows' && e) {
      switchToTab('runs'); loadRunsFiltered(repo, workflowNameFromFile(e)); return;
    }
  }
  // /runs/{id}/job/{jobId} (owner-less fallback)
  if (owner === 'runs' && seg.length >= 4 && seg[2] === 'job') {
    switchToTab('runs'); openRunById(repo, { jobID: parseInt(seg[3], 10), step: stepFromQuery() }); return;
  }

  switchToTab('runs'); loadRuns();
}

function stepFromQuery() {
  const v = new URLSearchParams(window.location.search).get('step');
  return v === null ? undefined : parseInt(v, 10);
}

window.addEventListener('popstate', () => render(window.location.pathname));

// Boot: check session before anything else
loadMe();

document.getElementById('logout-btn')?.addEventListener('click', doLogout);

// ── Boot: route from the current URL (legacy ?tab=/?repo= params still work) ──
(function boot() {
  const p = new URLSearchParams(window.location.search);
  const repo = p.get('repo');
  const workflow = p.get('workflow');
  if (repo || workflow) {
    switchToTab('runs');
    loadRunsFiltered(repo, workflow);
    return;
  }
  const tab = p.get('tab');
  if (tab && document.getElementById('tab-' + tab)) {
    pushURL(TAB_PATHS[tab] ?? '/');
    switchToTab(tab);
    TAB_LOADERS[tab]?.();
    return;
  }
  render(window.location.pathname);
})();

// ══════════════════════════════════════════════════════════════════════════════
// RUNS TAB
// ══════════════════════════════════════════════════════════════════════════════

async function loadRuns() {
  document.getElementById('runs-filter-title').classList.add('hidden');
  document.getElementById('clear-filter').classList.add('hidden');
  const resp = await apiFetch('/api/runs?limit=100');
  const runs = resp.ok ? await resp.json() : [];
  renderRunsTable(runs, document.getElementById('runs-body'));
  showRunsList();
}

async function loadRunsFiltered(repo, workflow) {
  const params = new URLSearchParams({ limit: 100 });
  if (repo)     params.set('repo',     repo);
  if (workflow) params.set('workflow', workflow);
  const resp = await apiFetch(`/api/runs?${params}`);
  const runs = resp.ok ? await resp.json() : [];
  const title = document.getElementById('runs-filter-title');
  title.textContent = [repo, workflow].filter(Boolean).join(' / ');
  title.classList.remove('hidden');
  document.getElementById('clear-filter').classList.remove('hidden');
  renderRunsTable(runs, document.getElementById('runs-body'));
  showRunsList();
}

function renderRunsTable(runs, tbody) {
  tbody.innerHTML = '';
  if (!runs.length) {
    const tr = document.createElement('tr');
    const td = document.createElement('td');
    td.colSpan = 7;
    td.textContent = 'No runs found.';
    td.style.color = 'var(--muted)';
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }
  for (const run of runs) {
    const tr = document.createElement('tr');
    tr.appendChild(makeTd(run.id));
    tr.appendChild(makeTd(run.repo));
    tr.appendChild(makeTd(run.workflow));
    tr.appendChild(makeTd(run.branch || '—'));
    tr.appendChild(makeTdHTML(badge(run.status)));
    tr.appendChild(makeTd(fmtTime(run.started_at)));
    tr.appendChild(makeTd(fmtDuration(run.started_at, run.finished_at)));
    tr.addEventListener('click', () => { pushURL(runURL(run)); openRun(run); });
    tbody.appendChild(tr);
  }
}

function showRunsList() {
  document.getElementById('runs-list-view').classList.remove('hidden');
  document.getElementById('run-detail').classList.add('hidden');
}

function showRunDetail() {
  document.getElementById('runs-list-view').classList.add('hidden');
  document.getElementById('run-detail').classList.remove('hidden');
}

async function selectJob(job, li) {
  document.querySelectorAll('.job-item').forEach(el => el.classList.remove('active'));
  li.classList.add('active');
  document.getElementById('steps-job-label').textContent = job.name;

  closeLogStream();
  const logBox = document.getElementById('log-output');
  logBox.textContent = '';
  logBox.classList.add('hidden');

  // Fetch steps for this job via the GH-compat endpoint which includes steps
  const resp = await apiFetch(`/api/runs/${job.run_id}/jobs`);
  let steps = [];
  if (resp.ok) {
    const jobs = await resp.json();
    const found = jobs.find(j => j.id === job.id);
    // The native /api/runs/{id}/jobs returns db.Job objects (no steps embedded).
    // Steps live under steps.job_id — we need to call per-job gh endpoint.
    // Fall back: steps embedded via gh compat endpoint isn't available on native path.
    // Use a direct steps fetch instead.
  }

  // Native path: fetch steps via a scoped query
  const stepsResp = await apiFetch(`/api/runs/${job.run_id}/jobs`);
  // The native endpoint returns db.Job objects without steps.
  // There is no native /api/jobs/{id}/steps endpoint.
  // We use the GH-compat endpoint to get the enriched job with steps.
  // But we don't know owner/repo here. Reconstruct from the run.
  // Simplest: we already have jobs from the run; re-fetch via gh-compat if we can.
  // Since we don't have owner/repo in scope here, use a workaround:
  // fetch all steps for the job via the step logs endpoint pattern.
  // The API exposes GET /api/steps/{id}/logs — we need step IDs.
  // The GH-compat endpoint /repos/{owner}/{repo}/actions/jobs/{id} returns steps.
  // But we don't expose a native /api/jobs/{id}/steps.
  // Solution: call the GH compat endpoint with a known run; we don't have owner.
  // BEST solution: just call /api/runs/{id}/jobs and use the step list from the
  // GH compat response embedded in the jobs list, or accept flat step rendering.
  //
  // Since the server's native /api/runs/{id}/jobs returns []db.Job (no steps),
  // we render placeholders and load logs directly when a step is clicked.
  // We'll synthesise "step" items from the job's status until steps API exists.

  // For now, render a single "view logs" step per job using step IDs from the
  // GH-compat /repos/{owner}/{repo}/actions/jobs/{id} endpoint which we CAN call
  // if we cached the repo/owner. Let's do it the right way: store owner+name on
  // the run detail open, then use gh-compat to get steps.
  renderStepsForJob(job);
}

// We cache the current run's repo owner+name for GH-compat calls
let _currentRunRepo = null; // { owner, name }

// _pendingSel carries {jobID, step} from the URL so refresh restores position.
let _pendingSel = null;

let _currentRun = null;

async function openRun(run, sel) {
  _pendingSel = sel || null;
  _currentRun = run;
  document.getElementById('cancel-run-btn').classList.toggle('hidden', run.status !== 'running');
  showRunDetail();
  document.getElementById('detail-title').textContent =
    `#${run.id} — ${run.workflow}`;
  document.getElementById('detail-sub').innerHTML =
    `<span class="meta-chip">${escText(run.repo)}</span>` +
    `<span class="meta-chip">${escText(run.branch || '—')}</span>` +
    (run.commit_sha ? `<span class="meta-chip">${escText(shortSHA(run.commit_sha))}</span>` : '') +
    `<span class="meta-chip">${makeBadgeHTML(run.status)}</span>`;

  closeLogStream();
  const logBox = document.getElementById('log-output');
  logBox.textContent = '';
  logBox.classList.add('hidden');
  document.getElementById('jobs-list').innerHTML = '';
  document.getElementById('steps-list').innerHTML = '';
  document.getElementById('steps-job-label').textContent = 'Steps';

  // Resolve owner/name — preferably from the repo cache (run.repo is usually
  // just the name), falling back to parsing an "owner/name" formatted value.
  const cached = repoByID(run.repo_id);
  const parts = (run.repo || '').split('/');
  _currentRunRepo = cached
    ? { owner: cached.owner, name: cached.name }
    : (parts.length >= 2 ? { owner: parts[0], name: parts.slice(1).join('/') } : null);

  const resp = await apiFetch(`/api/runs/${run.id}/jobs`);
  const jobs = resp.ok ? await resp.json() : [];

  if (!jobs.length) {
    const li = document.createElement('li');
    li.className = 'job-item job-empty';
    li.textContent = 'No jobs recorded yet.';
    document.getElementById('jobs-list').appendChild(li);
    return;
  }

  const jobsList = document.getElementById('jobs-list');
  for (const job of jobs) {
    const li = document.createElement('li');
    li.className = 'job-item';
    li.dataset.jobId = job.id;
    li.innerHTML =
      `${statusIcon(job.status)}<span class="job-name">${escText(job.name)}</span>` +
      `<span class="job-dur">${fmtDuration(job.started_at, job.finished_at)}</span>`;
    li.addEventListener('click', () => { pushJobURL(run, job); selectJob(job, li); });
    jobsList.appendChild(li);
  }

  // Select the URL's job if present, else the first one.
  let idx = 0;
  if (_pendingSel?.jobID) {
    const found = jobs.findIndex(j => j.id === _pendingSel.jobID);
    if (found >= 0) idx = found;
  }
  const lis = jobsList.querySelectorAll('.job-item');
  if (lis[idx]) selectJob(jobs[idx], lis[idx]);
}

// pushJobURL records the selected job in the path (GitHub shape:
// .../actions/runs/{id}/job/{job_id}); any ?step= is reset.
function pushJobURL(run, job) {
  const base = window.location.pathname.replace(/\/job\/\d+$/, '');
  const root = base.includes('/runs/') ? base : runURL(run);
  history.pushState({}, '', `${root}/job/${job.id}`);
}

// setStepQuery records the selected step index as ?step=N without adding
// history entries for every click.
function setStepQuery(i) {
  const u = new URL(window.location.href);
  u.searchParams.set('step', i);
  history.replaceState({}, '', u);
}

async function renderStepsForJob(job) {
  const stepsList = document.getElementById('steps-list');
  stepsList.innerHTML = '';
  document.getElementById('log-output').textContent = '';
  document.getElementById('log-output').classList.add('hidden');

  // Try GH-compat endpoint to get steps with their IDs
  let steps = null;
  if (_currentRunRepo) {
    const { owner, name } = _currentRunRepo;
    const r = await apiFetch(`/repos/${owner}/${name}/actions/jobs/${job.id}`);
    if (r.ok) {
      const ghJob = await r.json();
      steps = ghJob.steps || [];
      // GH-compat steps don't carry step DB IDs — we need native step IDs for streaming.
      // Store them so we can match by index when needed.
      steps._ghFormat = true;
    }
  }

  if (!steps || !steps.length) {
    // No steps data — show a "View logs" catch-all
    const li = document.createElement('li');
    li.className = 'step-item';
    li.innerHTML = statusIcon(job.status) + '<span class="step-name">All output</span>';
    li.addEventListener('click', () => loadLogsForJob(job));
    stepsList.appendChild(li);
    loadLogsForJob(job);
    return;
  }

  // We have GH-compat steps (by index/number). Map them to step DB IDs if possible.
  // Since we can't get step IDs from GH compat, we'll load job-level logs for all.
  for (let i = 0; i < steps.length; i++) {
    const step = steps[i];
    const li = document.createElement('li');
    li.className = 'step-item';
    li.dataset.idx = i;
    const rawStatus = ghStatusToRunway(step.status, step.conclusion);
    li.innerHTML =
      `${statusIcon(rawStatus)}` +
      `<span class="step-name">${escText(step.name)}</span>`;
    li.addEventListener('click', () => { setStepQuery(i); selectStep(step, rawStatus, job, li); });
    stepsList.appendChild(li);
  }

  // Select the URL's step if present (and consume it), else the first one.
  let idx = 0;
  if (Number.isInteger(_pendingSel?.step) && _pendingSel.step >= 0 && _pendingSel.step < steps.length) {
    idx = _pendingSel.step;
  }
  _pendingSel = null;
  const items = stepsList.querySelectorAll('.step-item');
  if (items[idx]) selectStep(steps[idx], ghStatusToRunway(steps[idx].status, steps[idx].conclusion), job, items[idx]);
}

function ghStatusToRunway(status, conclusion) {
  if (status === 'in_progress') return 'running';
  if (status === 'completed') return conclusion || 'success';
  return status || 'pending';
}

async function selectStep(step, status, job, li) {
  document.querySelectorAll('.step-item').forEach(el => el.classList.remove('active'));
  li.classList.add('active');

  closeLogStream();
  const logBox = document.getElementById('log-output');
  logBox.textContent = 'Loading…';
  logBox.classList.remove('hidden');

  // We load all job logs and filter by step number heuristically.
  // Since native step streaming uses step DB IDs which aren't in GH-compat
  // response, load all job logs via GH-compat endpoint.
  if (_currentRunRepo) {
    const { owner, name } = _currentRunRepo;
    const r = await apiFetch(`/repos/${owner}/${name}/actions/jobs/${job.id}/logs`);
    if (r.ok) {
      const text = await r.text();
      logBox.textContent = text || '(no output)';
      logBox.scrollTop = logBox.scrollHeight;
      return;
    }
  }

  // Last-resort fallback: load full run logs
  const r2 = await apiFetch(`/api/runs/${job.run_id}/logs`);
  if (r2.ok) {
    const text = await r2.text();
    logBox.textContent = text || '(no output)';
  } else {
    logBox.textContent = '(could not load logs)';
  }
  logBox.scrollTop = logBox.scrollHeight;
}

async function loadLogsForJob(job) {
  closeLogStream();
  const logBox = document.getElementById('log-output');
  logBox.textContent = 'Loading…';
  logBox.classList.remove('hidden');

  if (_currentRunRepo) {
    const { owner, name } = _currentRunRepo;
    const r = await apiFetch(`/repos/${owner}/${name}/actions/jobs/${job.id}/logs`);
    if (r.ok) {
      const text = await r.text();
      logBox.textContent = text || '(no output)';
      logBox.scrollTop = logBox.scrollHeight;
      return;
    }
  }

  // Fallback: full run logs
  const r2 = await apiFetch(`/api/runs/${job.run_id}/logs`);
  if (r2.ok) {
    const text = await r2.text();
    logBox.textContent = text || '(no output)';
  } else {
    logBox.textContent = '(could not load logs)';
  }
  logBox.scrollTop = logBox.scrollHeight;
}

function closeLogStream() {
  if (logStream) { logStream.close(); logStream = null; }
}

// Scroll-to-top button for long logs. The log box grows with its content and
// the PAGE scrolls, so this watches window scroll while a run detail is open.
(function wireLogTopButton() {
  const btn = document.getElementById('log-top-btn');
  if (!btn) return;
  window.addEventListener('scroll', () => {
    const detailOpen = !document.getElementById('run-detail').classList.contains('hidden');
    btn.classList.toggle('hidden', !(detailOpen && window.scrollY > 400));
  }, { passive: true });
  btn.addEventListener('click', () => window.scrollTo({ top: 0, behavior: 'smooth' }));
})();

// ── Helpers shared with the outer and inner openRun ───────────────────────────
function makeBadgeHTML(status) {
  const safe = String(status).replace(/[^a-z_]/g, '');
  return `<span class="badge badge-${safe}">${safe}</span>`;
}

function escText(s) {
  const d = document.createElement('span');
  d.textContent = String(s ?? '');
  return d.innerHTML;
}

document.getElementById('rerun-btn').addEventListener('click', async () => {
  if (!_currentRun) return;
  const repo = repoByID(_currentRun.repo_id);
  if (!repo) return alert('Repo unknown for this run.');
  const r = await apiFetch(`/repos/${repo.owner}/${repo.name}/actions/runs/${_currentRun.id}/rerun`, { method: 'POST' });
  if (r.ok) { pushURL('/queue'); switchToTab('queue'); loadQueue(); }
  else alert('Re-run failed.');
});

document.getElementById('cancel-run-btn').addEventListener('click', async () => {
  if (!_currentRun) return;
  const repo = repoByID(_currentRun.repo_id);
  if (!repo) return;
  if (!confirm('Cancel this running workflow?')) return;
  const r = await apiFetch(`/repos/${repo.owner}/${repo.name}/actions/runs/${_currentRun.id}/cancel`, { method: 'POST' });
  if (!r.ok && r.status !== 202) alert('Cancel failed (run may have already finished).');
  setTimeout(() => openRunById(_currentRun.id), 1200);
});

document.getElementById('close-detail').addEventListener('click', () => {
  closeLogStream();
  pushURL('/');
  showRunsList();
});

document.getElementById('refresh-runs').addEventListener('click', () => { pushURL('/'); loadRuns(); });
document.getElementById('clear-filter').addEventListener('click', () => { pushURL('/'); loadRuns(); });

// ══════════════════════════════════════════════════════════════════════════════
// QUEUE TAB
// ══════════════════════════════════════════════════════════════════════════════

async function loadQueue() {
  const resp = await apiFetch('/api/queue');
  const items = resp.ok ? await resp.json() : [];
  renderQueueTable(items);
}

function renderQueueTable(items) {
  const tbody = document.getElementById('queue-body');
  tbody.innerHTML = '';
  const active = items.filter(i => i.status === 'queued' || i.status === 'running');
  if (!active.length) {
    const tr = document.createElement('tr');
    const td = document.createElement('td');
    td.colSpan = 7;
    td.textContent = 'Queue is empty.';
    td.style.color = 'var(--muted)';
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }
  active.forEach((item, idx) => {
    const tr = document.createElement('tr');
    tr.appendChild(makeTd(idx + 1));
    tr.appendChild(makeTd(item.workflow_file));
    tr.appendChild(makeTd(item.branch));
    tr.appendChild(makeTdHTML(badge(item.status)));
    tr.appendChild(makeTd(fmtTime(item.created_at)));
    tr.appendChild(makeTd(fmtWait(item.created_at)));

    const cancelTd = document.createElement('td');
    if (item.status === 'queued' || item.status === 'running') {
      const btn = document.createElement('button');
      btn.textContent = 'Cancel';
      btn.className = 'btn-danger btn-sm';
      btn.addEventListener('click', async (e) => {
        e.stopPropagation();
        btn.disabled = true;
        const r = await apiFetch(`/api/queue/${item.id}`, { method: 'DELETE' });
        if (r.ok || r.status === 204) {
          loadQueue();
        } else {
          btn.disabled = false;
          alert('Failed to cancel queue item.');
        }
      });
      cancelTd.appendChild(btn);
    }
    tr.appendChild(cancelTd);
    tbody.appendChild(tr);
  });
}

document.getElementById('refresh-queue').addEventListener('click', loadQueue);

// ══════════════════════════════════════════════════════════════════════════════
// REPOS TAB
// ══════════════════════════════════════════════════════════════════════════════

let _currentRepo = null;

function showReposList() {
  document.getElementById('repos-list-view').classList.remove('hidden');
  document.getElementById('repos-detail-view').classList.add('hidden');
}

async function loadRepos() {
  const resp = await apiFetch('/api/repos');
  const repos = resp.ok ? await resp.json() : [];
  const tbody = document.getElementById('repos-body');
  tbody.innerHTML = '';
  if (!repos.length) {
    const tr = document.createElement('tr');
    const td = document.createElement('td');
    td.colSpan = 6;
    td.textContent = 'No repositories registered.';
    td.style.color = 'var(--muted)';
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }
  for (const repo of repos) {
    const tr = document.createElement('tr');
    tr.appendChild(makeTd(repo.name));
    tr.appendChild(makeTd(repo.owner));
    const urlTd = document.createElement('td');
    urlTd.textContent = repo.git_url;
    urlTd.style.maxWidth = '260px';
    urlTd.style.overflow = 'hidden';
    urlTd.style.textOverflow = 'ellipsis';
    urlTd.title = repo.git_url;
    tr.appendChild(urlTd);
    tr.appendChild(makeTd(repo.default_branch));
    tr.appendChild(makeTd(fmtTime(repo.created_at)));

    const actTd = document.createElement('td');
    actTd.style.width = '80px';
    const viewBtn = document.createElement('button');
    viewBtn.textContent = 'View';
    viewBtn.addEventListener('click', (e) => { e.stopPropagation(); openRepo(repo); });
    actTd.appendChild(viewBtn);
    tr.appendChild(actTd);

    tr.addEventListener('click', () => openRepo(repo));
    tbody.appendChild(tr);
  }
}

async function openRepo(repo, view) {
  _currentRepo = repo;
  _currentScope = '';
  // Keep the URL on this repo (don't clobber a deeper /tree|blob/... path).
  const base = `/${repo.owner}/${repo.name}`;
  if (!window.location.pathname.startsWith(base)) pushURL(base);
  loadScopes(repo);
  loadGitStatus(repo);
  if (view?.pane === 'files') {
    showRepoPane('files');
    if (view.mode === 'blob') loadBlob(repo, view.path || '');
    else loadFiles(repo, view.path || '');
  } else if (view?.pane === 'secrets') {
    showRepoPane('secrets');
  } else {
    showRepoPane('overview');
  }
  document.getElementById('repos-list-view').classList.add('hidden');
  document.getElementById('repos-detail-view').classList.remove('hidden');
  document.getElementById('repos-detail-title').textContent = `${repo.owner}/${repo.name}`;

  document.getElementById('repo-detail-meta').innerHTML =
    `<span class="meta-chip">${escText(repo.git_url)}</span>` +
    `<span class="meta-chip">branch: ${escText(repo.default_branch)}</span>` +
    (repo.clone_path ? `<span class="meta-chip">cloned: ${escText(repo.clone_path)}</span>` : '');

  loadRepoWorkflows(repo);
  loadRepoRuns(repo);
}

async function loadRepoWorkflows(repo) {
  const tbody = document.getElementById('workflows-body');
  tbody.innerHTML = '<tr><td colspan="5" style="color:var(--muted)">Loading…</td></tr>';

  const resp = await apiFetch(`/api/repos/${repo.id}/workflows`);
  const workflows = resp.ok ? await resp.json() : [];
  tbody.innerHTML = '';

  if (!workflows.length) {
    const tr = document.createElement('tr');
    const td = document.createElement('td');
    td.colSpan = 5;
    td.textContent = 'No workflows found. Use "Sync Workflows" after first run.';
    td.style.color = 'var(--muted)';
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }

  for (const wf of workflows) {
    const tr = document.createElement('tr');
    tr.appendChild(makeTd(wf.name));
    tr.appendChild(makeTd(wf.file));
    tr.appendChild(makeTd(wf.run_count));
    tr.appendChild(makeTdHTML(wf.last_status ? badge(wf.last_status) : '—'));

    const actTd = document.createElement('td');
    const dispatchBtn = document.createElement('button');
    dispatchBtn.textContent = 'Dispatch';
    dispatchBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      openDispatchModal(repo, wf);
    });
    actTd.appendChild(dispatchBtn);
    tr.appendChild(actTd);

    tr.addEventListener('click', () => {
      pushURL(`/${repo.owner}/${repo.name}/actions/workflows/${wf.file}`);
      switchToTab('runs');
      loadRunsFiltered(repo.name, wf.name);
    });
    tbody.appendChild(tr);
  }
}

async function loadRepoRuns(repo) {
  const params = new URLSearchParams({ limit: 20, repo: repo.name });
  const resp = await apiFetch(`/api/runs?${params}`);
  const runs = resp.ok ? await resp.json() : [];
  renderRunsTable(runs, document.getElementById('repo-runs-body'));
}

// ── Repo detail panes (Overview | Files) + sync status + file browser ─────────

function showRepoPane(pane) {
  for (const name of ['overview', 'files', 'secrets']) {
    document.getElementById('repo-tab-' + name).classList.toggle('active', pane === name);
    document.getElementById('repo-' + name).classList.toggle('hidden', pane !== name);
  }
}

document.getElementById('repo-tab-overview').addEventListener('click', () => {
  if (!_currentRepo) return;
  pushURL(`/${_currentRepo.owner}/${_currentRepo.name}`);
  showRepoPane('overview');
});

document.getElementById('repo-tab-files').addEventListener('click', () => {
  if (!_currentRepo) return;
  pushURL(`/${_currentRepo.owner}/${_currentRepo.name}/tree/${_currentRepo.default_branch}`);
  showRepoPane('files');
  loadFiles(_currentRepo, '');
});

document.getElementById('repo-tab-secrets').addEventListener('click', () => {
  if (!_currentRepo) return;
  pushURL(`/${_currentRepo.owner}/${_currentRepo.name}/settings/secrets`);
  showRepoPane('secrets');
  loadScopes(_currentRepo);
});

async function loadGitStatus(repo) {
  const el = document.getElementById('repo-sync');
  el.className = 'sync-badge sync-unknown';
  el.textContent = 'checking origin…';
  const r = await apiFetch(`/api/repos/${repo.id}/gitstatus`);
  if (!r.ok) { el.textContent = ''; return; }
  const st = await r.json();
  if (!st.cloned) {
    el.textContent = 'not cloned yet';
    return;
  }
  if (st.synced) {
    el.className = 'sync-badge sync-ok';
    el.textContent = `✓ in sync with origin/${st.branch} (${(st.local_sha || '').slice(0, 7)})`;
  } else if (st.behind > 0) {
    el.className = 'sync-badge sync-behind';
    el.textContent = `↓ ${st.behind} behind origin/${st.branch} — pull needed (next run pulls automatically)`;
  } else if (st.ahead > 0) {
    el.className = 'sync-badge sync-behind';
    el.textContent = `↑ ${st.ahead} ahead of origin/${st.branch} (local changes?)`;
  } else {
    el.textContent = `origin/${st.branch} unreachable`;
  }
}

function renderCrumb(repo, path, mode) {
  const crumb = document.getElementById('files-crumb');
  crumb.innerHTML = '';
  const mk = (label, target) => {
    const a = document.createElement('a');
    a.textContent = label;
    a.addEventListener('click', () => {
      pushURL(`/${repo.owner}/${repo.name}/tree/${repo.default_branch}${target ? '/' + target : ''}`);
      loadFiles(repo, target);
    });
    return a;
  };
  crumb.appendChild(mk(repo.name, ''));
  const parts = path ? path.split('/') : [];
  let acc = '';
  for (let i = 0; i < parts.length; i++) {
    const sep = document.createElement('span');
    sep.className = 'crumb-sep';
    sep.textContent = '/';
    crumb.appendChild(sep);
    acc = acc ? acc + '/' + parts[i] : parts[i];
    if (i === parts.length - 1 && mode === 'blob') {
      const span = document.createElement('span');
      span.textContent = parts[i];
      crumb.appendChild(span);
    } else {
      crumb.appendChild(mk(parts[i], acc));
    }
  }
}

function fmtSize(n) {
  if (!n) return '';
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1024 / 1024).toFixed(1) + ' MB';
}

async function loadFiles(repo, path) {
  document.getElementById('file-view').classList.add('hidden');
  document.getElementById('files-table').classList.remove('hidden');
  renderCrumb(repo, path, 'tree');
  const tbody = document.getElementById('files-body');
  tbody.innerHTML = '<tr><td colspan="3" style="color:var(--muted)">Loading…</td></tr>';
  const r = await apiFetch(`/repos/${repo.owner}/${repo.name}/contents/${path}`);
  if (!r.ok) {
    const err = await r.json().catch(() => ({}));
    tbody.innerHTML = '';
    const tr = document.createElement('tr');
    const td = document.createElement('td');
    td.colSpan = 3;
    td.style.color = 'var(--muted)';
    td.textContent = err.error || 'Could not load contents.';
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }
  const entries = await r.json();
  tbody.innerHTML = '';
  for (const e of entries) {
    const tr = document.createElement('tr');
    const icon = document.createElement('td');
    icon.className = 'file-icon';
    icon.textContent = e.type === 'dir' ? '📁' : '📄';
    tr.appendChild(icon);
    tr.appendChild(makeTd(e.name));
    tr.appendChild(makeTd(e.type === 'dir' ? '' : fmtSize(e.size)));
    tr.addEventListener('click', () => {
      const kind = e.type === 'dir' ? 'tree' : 'blob';
      pushURL(`/${repo.owner}/${repo.name}/${kind}/${repo.default_branch}/${e.path}`);
      if (e.type === 'dir') loadFiles(repo, e.path);
      else loadBlob(repo, e.path);
    });
    tbody.appendChild(tr);
  }
}

async function loadBlob(repo, path) {
  renderCrumb(repo, path, 'blob');
  document.getElementById('files-table').classList.add('hidden');
  const view = document.getElementById('file-view');
  view.classList.remove('hidden');
  view.textContent = 'Loading…';
  const r = await apiFetch(`/repos/${repo.owner}/${repo.name}/contents/${path}`);
  if (!r.ok) {
    const err = await r.json().catch(() => ({}));
    view.textContent = err.error || 'Could not load file.';
    return;
  }
  const f = await r.json();
  let text;
  try {
    // atob → bytes → UTF-8 so multibyte characters render correctly.
    const bin = atob(f.content || '');
    const bytes = Uint8Array.from(bin, ch => ch.charCodeAt(0));
    text = new TextDecoder().decode(bytes);
  } catch {
    view.textContent = '(binary file)';
    return;
  }
  if (/\x00/.test(text)) {
    view.textContent = `(binary file, ${fmtSize(f.size)})`;
    return;
  }
  view.textContent = '';
  for (const line of text.split('\n')) {
    const span = document.createElement('span');
    span.className = 'ln';
    span.textContent = line === '' ? ' ' : line;
    view.appendChild(span);
  }
}

// ── Secrets / variables / environments (GitHub-compatible endpoints) ──────────
let _currentScope = ''; // '' = repository scope, otherwise environment name

// scopePath builds the GitHub-shaped endpoint for the active scope:
// repo level lives under /actions/{kind}, environment level under
// /environments/{env}/{kind} (no /actions segment — same as GitHub).
function scopePath(repo, kind, name) {
  const root = `/repos/${repo.owner}/${repo.name}`;
  const prefix = _currentScope
    ? `${root}/environments/${encodeURIComponent(_currentScope)}`
    : `${root}/actions`;
  return `${prefix}/${kind}${name ? '/' + encodeURIComponent(name) : ''}`;
}

async function loadScopes(repo) {
  const sel = document.getElementById('env-scope');
  const resp = await apiFetch(`/repos/${repo.owner}/${repo.name}/environments`);
  const envs = resp.ok ? (await resp.json()).environments || [] : [];
  sel.innerHTML = '';
  const optRepo = document.createElement('option');
  optRepo.value = '';
  optRepo.textContent = 'Repository';
  sel.appendChild(optRepo);
  for (const e of envs) {
    const o = document.createElement('option');
    o.value = e.name;
    o.textContent = 'env: ' + e.name;
    sel.appendChild(o);
  }
  sel.value = _currentScope;
  document.getElementById('delete-env-btn').classList.toggle('hidden', !_currentScope);
  loadSecretsAndVars(repo);
}

async function loadSecretsAndVars(repo) {
  const sBody = document.getElementById('secrets-body');
  const vBody = document.getElementById('vars-body');
  sBody.innerHTML = '';
  vBody.innerHTML = '';

  const [sResp, vResp] = await Promise.all([
    apiFetch(scopePath(repo, 'secrets')),
    apiFetch(scopePath(repo, 'variables')),
  ]);
  const secrets = sResp.ok ? (await sResp.json()).secrets || [] : [];
  const vars = vResp.ok ? (await vResp.json()).variables || [] : [];

  const emptyRow = (tbody, cols, text) => {
    const tr = document.createElement('tr');
    const td = document.createElement('td');
    td.colSpan = cols; td.textContent = text; td.style.color = 'var(--muted)';
    tr.appendChild(td); tbody.appendChild(tr);
  };

  if (!secrets.length) emptyRow(sBody, 3, 'No secrets in this scope.');
  for (const s of secrets) {
    const tr = document.createElement('tr');
    tr.appendChild(makeTd(s.name));
    tr.appendChild(makeTd(fmtTime(s.updated_at)));
    const td = document.createElement('td');
    const del = document.createElement('button');
    del.textContent = 'Delete';
    del.className = 'btn-danger btn-sm';
    del.addEventListener('click', async () => {
      if (!confirm(`Delete secret ${s.name}?`)) return;
      await apiFetch(scopePath(repo, 'secrets', s.name), { method: 'DELETE' });
      loadSecretsAndVars(repo);
    });
    td.appendChild(del);
    tr.appendChild(td);
    sBody.appendChild(tr);
  }

  if (!vars.length) emptyRow(vBody, 3, 'No variables in this scope.');
  for (const v of vars) {
    const tr = document.createElement('tr');
    tr.appendChild(makeTd(v.name));
    tr.appendChild(makeTd(v.value));
    const td = document.createElement('td');
    const del = document.createElement('button');
    del.textContent = 'Delete';
    del.className = 'btn-danger btn-sm';
    del.addEventListener('click', async () => {
      if (!confirm(`Delete variable ${v.name}?`)) return;
      await apiFetch(scopePath(repo, 'variables', v.name), { method: 'DELETE' });
      loadSecretsAndVars(repo);
    });
    td.appendChild(del);
    tr.appendChild(td);
    vBody.appendChild(tr);
  }
}

document.getElementById('env-scope').addEventListener('change', (e) => {
  _currentScope = e.target.value;
  document.getElementById('delete-env-btn').classList.toggle('hidden', !_currentScope);
  if (_currentRepo) loadSecretsAndVars(_currentRepo);
});

document.getElementById('add-env-btn').addEventListener('click', async () => {
  if (!_currentRepo) return;
  const name = prompt('Environment name (e.g. production, staging):');
  if (!name) return;
  const r = await apiFetch(`/repos/${_currentRepo.owner}/${_currentRepo.name}/environments/${encodeURIComponent(name)}`, { method: 'PUT' });
  if (r.ok) { _currentScope = name; loadScopes(_currentRepo); }
  else alert('Failed to create environment.');
});

document.getElementById('delete-env-btn').addEventListener('click', async () => {
  if (!_currentRepo || !_currentScope) return;
  if (!confirm(`Delete environment "${_currentScope}" and all its secrets/variables?`)) return;
  const r = await apiFetch(`/repos/${_currentRepo.owner}/${_currentRepo.name}/environments/${encodeURIComponent(_currentScope)}`, { method: 'DELETE' });
  if (r.ok || r.status === 204) { _currentScope = ''; loadScopes(_currentRepo); }
  else alert('Failed to delete environment.');
});

document.getElementById('add-secret-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  if (!_currentRepo) return;
  const form = e.target;
  const msg = document.getElementById('add-secret-msg');
  const r = await apiFetch(scopePath(_currentRepo, 'secrets', form.name.value.trim()), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value: form.value.value }),
  });
  msg.style.color = r.ok ? 'var(--success)' : 'var(--failure)';
  msg.textContent = r.ok ? 'Saved.' : 'Error: ' + ((await r.json().catch(() => ({})))?.error || r.status);
  if (r.ok) { form.reset(); loadSecretsAndVars(_currentRepo); }
  setTimeout(() => { msg.textContent = ''; }, 3000);
});

document.getElementById('add-var-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  if (!_currentRepo) return;
  const form = e.target;
  const msg = document.getElementById('add-var-msg');
  const r = await apiFetch(scopePath(_currentRepo, 'variables'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: form.name.value.trim(), value: form.value.value }),
  });
  msg.style.color = r.ok ? 'var(--success)' : 'var(--failure)';
  msg.textContent = r.ok ? 'Saved.' : 'Error: ' + ((await r.json().catch(() => ({})))?.error || r.status);
  if (r.ok) { form.reset(); loadSecretsAndVars(_currentRepo); }
  setTimeout(() => { msg.textContent = ''; }, 3000);
});

document.getElementById('repos-back').addEventListener('click', () => {
  pushURL('/repos');
  showReposList();
  loadRepos();
});

document.getElementById('delete-repo-btn').addEventListener('click', async () => {
  if (!_currentRepo) return;
  if (!confirm(`Delete repo "${_currentRepo.owner}/${_currentRepo.name}"? This will remove all associated runs, jobs, and logs.`)) return;
  const resp = await apiFetch(`/api/repos/${_currentRepo.id}`, { method: 'DELETE' });
  if (resp.ok || resp.status === 204) {
    showReposList();
    loadRepos();
  } else {
    alert('Failed to delete repository.');
  }
});

document.getElementById('sync-repo-btn').addEventListener('click', async () => {
  if (!_currentRepo) return;
  const btn = document.getElementById('sync-repo-btn');
  btn.disabled = true;
  btn.textContent = 'Syncing…';
  const resp = await apiFetch(`/api/repos/${_currentRepo.id}/sync`, { method: 'POST' });
  btn.disabled = false;
  btn.textContent = '↻ Sync Workflows';
  if (resp.ok) {
    loadRepoWorkflows(_currentRepo);
  } else {
    const err = resp.ok ? null : await resp.json().catch(() => ({}));
    alert('Sync failed: ' + (err?.error || resp.status));
  }
});

// ── Add Repo Modal ────────────────────────────────────────────────────────────
document.getElementById('add-repo-btn').addEventListener('click', () => {
  document.getElementById('add-repo-modal').classList.remove('hidden');
  document.getElementById('add-repo-msg').textContent = '';
  document.getElementById('add-repo-form').reset();
});

document.getElementById('add-repo-close').addEventListener('click', () => {
  document.getElementById('add-repo-modal').classList.add('hidden');
});

document.getElementById('add-repo-modal').addEventListener('click', (e) => {
  if (e.target === document.getElementById('add-repo-modal')) {
    document.getElementById('add-repo-modal').classList.add('hidden');
  }
});

document.getElementById('add-repo-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const form   = e.target;
  const msg    = document.getElementById('add-repo-msg');
  const submit = form.querySelector('[type=submit]');

  const gitURL = form.git_url.value.trim();
  const parsed = parseGitURL(gitURL);
  if (!parsed) {
    msg.style.color = 'var(--failure)';
    msg.textContent = 'Could not parse owner/name from URL. Use https://github.com/owner/repo.git';
    return;
  }
  const deployKeyRaw = form.deploy_key.value.trim();
  const body = {
    git_url:        gitURL,
    owner:          parsed.owner,
    name:           parsed.name,
    default_branch: form.default_branch.value.trim() || 'main',
  };
  if (deployKeyRaw) body.deploy_key = deployKeyRaw;

  submit.disabled = true;
  msg.textContent = 'Adding…';

  const resp = await apiFetch('/api/repos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  submit.disabled = false;

  if (resp.ok || resp.status === 201) {
    msg.style.color = 'var(--success)';
    msg.textContent = 'Repository added.';
    setTimeout(() => {
      document.getElementById('add-repo-modal').classList.add('hidden');
      loadRepos();
    }, 800);
  } else {
    const err = await resp.json().catch(() => ({}));
    msg.style.color = 'var(--failure)';
    msg.textContent = err.error || `Error ${resp.status}`;
  }
});

// ── Dispatch Modal ────────────────────────────────────────────────────────────
function openDispatchModal(repo, wf) {
  const modal = document.getElementById('dispatch-modal');
  const form  = document.getElementById('dispatch-form');
  form.owner.value         = repo.owner;
  form.repo_name.value     = repo.name;
  form.workflow_file.value = wf.file;
  form.ref.value           = repo.default_branch || 'main';
  form.inputs.value        = '';
  document.getElementById('dispatch-modal-title').textContent =
    `Dispatch: ${wf.name}`;
  document.getElementById('dispatch-msg').textContent = '';
  modal.classList.remove('hidden');
}

document.getElementById('dispatch-close').addEventListener('click', () => {
  document.getElementById('dispatch-modal').classList.add('hidden');
});

document.getElementById('dispatch-modal').addEventListener('click', (e) => {
  if (e.target === document.getElementById('dispatch-modal')) {
    document.getElementById('dispatch-modal').classList.add('hidden');
  }
});

document.getElementById('dispatch-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const form   = e.target;
  const msg    = document.getElementById('dispatch-msg');
  const submit = form.querySelector('[type=submit]');

  const owner    = form.owner.value;
  const repoName = form.repo_name.value;
  const wfFile   = form.workflow_file.value;
  const ref      = form.ref.value.trim();
  const rawInputs = form.inputs.value.trim();

  let inputs = {};
  if (rawInputs) {
    try { inputs = JSON.parse(rawInputs); }
    catch {
      msg.style.color = 'var(--failure)';
      msg.textContent = 'Inputs must be valid JSON.';
      return;
    }
  }

  submit.disabled = true;
  msg.textContent = 'Dispatching…';

  const resp = await apiFetch(
    `/repos/${owner}/${repoName}/actions/workflows/${wfFile}/dispatches`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ref, inputs }),
    }
  );
  submit.disabled = false;

  if (resp.ok || resp.status === 204) {
    msg.style.color = 'var(--success)';
    msg.textContent = 'Dispatched.';
    setTimeout(() => {
      document.getElementById('dispatch-modal').classList.add('hidden');
    }, 800);
  } else {
    const err = await resp.json().catch(() => ({}));
    msg.style.color = 'var(--failure)';
    msg.textContent = err.error || `Error ${resp.status}`;
  }
});

// ══════════════════════════════════════════════════════════════════════════════
// SETTINGS TAB
// ══════════════════════════════════════════════════════════════════════════════

async function loadSettings() {
  const resp = await apiFetch('/api/settings');
  if (!resp.ok) return;
  const s = await resp.json();
  const form = document.getElementById('settings-form');
  if (s.retention_days)  form.retention_days.value  = s.retention_days;
  if (s.concurrency)     form.concurrency.value     = s.concurrency;
  if (s.act_platform_mappings) form.act_platform_mappings.value = s.act_platform_mappings;
  if (s.docker_memory)   form.docker_memory.value   = s.docker_memory;
  if (s.docker_cpus)     form.docker_cpus.value     = s.docker_cpus;
  if (s.act_container_options) form.act_container_options.value = s.act_container_options;
  if (s.notify_webhook_url) form.notify_webhook_url.value = s.notify_webhook_url;
  if (s.notify_on)          form.notify_on.value = s.notify_on;
  if (s.webhook_secret)     form.webhook_secret.value = s.webhook_secret;
  if (s.artifacts_dir)      form.artifacts_dir.value = s.artifacts_dir;

  renderRLInfo();
}

document.getElementById('settings-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const form   = e.target;
  const msg    = document.getElementById('settings-msg');
  const submit = form.querySelector('[type=submit]');
  submit.disabled = true;

  const payload = {
    retention_days:       form.retention_days.value,
    concurrency:          form.concurrency.value,
    act_platform_mappings: form.act_platform_mappings.value.trim(),
    docker_memory:        form.docker_memory.value.trim(),
    docker_cpus:          form.docker_cpus.value.trim(),
    act_container_options: form.act_container_options.value.trim(),
    notify_webhook_url: form.notify_webhook_url.value.trim(),
    notify_on:          form.notify_on.value,
    webhook_secret:     form.webhook_secret.value.trim(),
    artifacts_dir:      form.artifacts_dir.value.trim(),
  };

  const resp = await apiFetch('/api/settings', {
    method:  'POST',
    headers: { 'Content-Type': 'application/json' },
    body:    JSON.stringify(payload),
  });
  submit.disabled = false;
  msg.style.color = resp.ok || resp.status === 204 ? 'var(--success)' : 'var(--failure)';
  msg.textContent = resp.ok || resp.status === 204 ? 'Saved.' : 'Error saving settings.';
  setTimeout(() => { msg.textContent = ''; }, 3000);
});

// ══════════════════════════════════════════════════════════════════════════════
// CHANGE PASSWORD (banner form)
// ══════════════════════════════════════════════════════════════════════════════

document.getElementById('change-pw-form')?.addEventListener('submit', async (e) => {
  e.preventDefault();
  const form = e.target;
  const msg  = document.getElementById('change-pw-msg');
  const body = {
    current_password: form.current_password.value,
    new_password:     form.new_password.value,
  };
  const resp = await apiFetch('/api/me/password', {
    method:  'POST',
    headers: { 'Content-Type': 'application/json' },
    body:    JSON.stringify(body),
  });
  if (resp.ok) {
    document.getElementById('change-pw-banner').classList.add('hidden');
    if (_currentUser) _currentUser.must_change_pw = false;
    form.reset();
  } else {
    const err = await resp.json().catch(() => ({}));
    msg.style.color = 'var(--danger)';
    msg.textContent = err.error || 'Failed to change password.';
  }
});

// ══════════════════════════════════════════════════════════════════════════════
// USERS TAB (admin only)
// ══════════════════════════════════════════════════════════════════════════════

async function loadUsers() {
  if (!_currentUser || _currentUser.role !== 'admin') return;
  const resp = await apiFetch('/api/users');
  const users = resp.ok ? await resp.json() : [];
  const tbody = document.getElementById('users-body');
  if (!tbody) return;
  tbody.innerHTML = '';
  for (const u of users) {
    const tr = document.createElement('tr');
    tr.appendChild(makeTd(u.username));
    tr.appendChild(makeTd(u.role));
    tr.appendChild(makeTd(u.must_change_pw ? 'Yes' : 'No'));
    tr.appendChild(makeTd(fmtTime(u.created_at)));
    const actTd = document.createElement('td');
    if (u.id !== _currentUser?.id) {
      const del = document.createElement('button');
      del.textContent = 'Delete';
      del.className = 'btn-danger btn-sm';
      del.addEventListener('click', async () => {
        if (!confirm(`Delete user "${u.username}"?`)) return;
        const r = await apiFetch(`/api/users/${u.id}`, { method: 'DELETE' });
        if (r.ok || r.status === 204) loadUsers();
      });
      actTd.appendChild(del);
    }
    tr.appendChild(actTd);
    tbody.appendChild(tr);
  }
}

document.getElementById('add-user-form')?.addEventListener('submit', async (e) => {
  e.preventDefault();
  const form = e.target;
  const msg  = document.getElementById('add-user-msg');
  const body = {
    username: form.username.value.trim(),
    password: form.password.value,
    role:     form.role.value,
  };
  const resp = await apiFetch('/api/users', {
    method:  'POST',
    headers: { 'Content-Type': 'application/json' },
    body:    JSON.stringify(body),
  });
  if (resp.ok || resp.status === 201) {
    form.reset();
    msg.style.color = 'var(--success)';
    msg.textContent = 'User created.';
    loadUsers();
    setTimeout(() => { msg.textContent = ''; }, 3000);
  } else {
    const err = await resp.json().catch(() => ({}));
    msg.style.color = 'var(--danger)';
    msg.textContent = err.error || 'Error creating user.';
  }
});

// ── API Keys ──────────────────────────────────────────────────────────────────

async function loadAPIKeys() {
  const resp = await apiFetch('/api/keys');
  const keys = resp.ok ? await resp.json() : [];
  const tbody = document.getElementById('keys-body');
  if (!tbody) return;
  tbody.innerHTML = '';
  if (!keys.length) {
    const tr = document.createElement('tr');
    const td = document.createElement('td');
    td.colSpan = 4;
    td.textContent = 'No API keys yet.';
    td.style.color = 'var(--muted)';
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }
  for (const k of keys) {
    const tr = document.createElement('tr');
    tr.appendChild(makeTd(k.name));
    tr.appendChild(makeTd(fmtTime(k.created_at)));
    tr.appendChild(makeTd(k.last_used_at ? fmtTime(k.last_used_at) : 'Never'));
    const actTd = document.createElement('td');
    const del = document.createElement('button');
    del.textContent = 'Revoke';
    del.className = 'btn-danger btn-sm';
    del.addEventListener('click', async () => {
      if (!confirm(`Revoke key "${k.name}"?`)) return;
      const r = await apiFetch(`/api/keys/${k.id}`, { method: 'DELETE' });
      if (r.ok || r.status === 204) loadAPIKeys();
    });
    actTd.appendChild(del);
    tr.appendChild(actTd);
    tbody.appendChild(tr);
  }
}

document.getElementById('create-key-form')?.addEventListener('submit', async (e) => {
  e.preventDefault();
  const form = e.target;
  const msg  = document.getElementById('create-key-msg');
  const resp = await apiFetch('/api/keys', {
    method:  'POST',
    headers: { 'Content-Type': 'application/json' },
    body:    JSON.stringify({ name: form.key_name.value.trim() }),
  });
  if (resp.ok || resp.status === 201) {
    const data = await resp.json();
    form.reset();
    // Show the key once in a dismissible box
    const box = document.getElementById('new-key-box');
    const val = document.getElementById('new-key-value');
    if (box && val) {
      val.textContent = data.key;
      box.classList.remove('hidden');
    }
    loadAPIKeys();
  } else {
    const err = await resp.json().catch(() => ({}));
    msg.style.color = 'var(--danger)';
    msg.textContent = err.error || 'Error.';
  }
});

document.getElementById('dismiss-key-btn')?.addEventListener('click', () => {
  document.getElementById('new-key-box')?.classList.add('hidden');
});

// Wire change-password form on the Users tab
document.getElementById('change-pw-form-tab')?.addEventListener('submit', async (e) => {
  e.preventDefault();
  const form = e.target;
  const msg  = document.getElementById('change-pw-tab-msg');
  const resp = await apiFetch('/api/me/password', {
    method:  'POST',
    headers: { 'Content-Type': 'application/json' },
    body:    JSON.stringify({
      current_password: form.current_password.value,
      new_password:     form.new_password.value,
    }),
  });
  if (resp.ok) {
    form.reset();
    msg.style.color = 'var(--success)';
    msg.textContent = 'Password changed.';
    document.getElementById('change-pw-banner')?.classList.add('hidden');
    setTimeout(() => { msg.textContent = ''; }, 3000);
  } else {
    const err = await resp.json().catch(() => ({}));
    msg.style.color = 'var(--danger)';
    msg.textContent = err.error || 'Failed.';
  }
});

// ══════════════════════════════════════════════════════════════════════════════
// AUTO-REFRESH
// ══════════════════════════════════════════════════════════════════════════════

setInterval(() => {
  const onRuns  = document.getElementById('tab-runs').classList.contains('active');
  const inDetail = !document.getElementById('run-detail').classList.contains('hidden');
  if (onRuns && !inDetail) loadRuns();
}, 10000);

setInterval(() => {
  const onQueue = document.getElementById('tab-queue').classList.contains('active');
  if (onQueue) loadQueue();
}, 5000);
