import { init, Terminal as GhosttyTerminal, FitAddon } from 'ghostty-web';

// Initialize ghostty-web WASM (must complete before creating terminals)
const ghosttyReady = init();

// --- Theme ---

function getPreferredTheme() {
  const stored = localStorage.getItem("theme");
  if (stored) return stored;
  return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

function applyTheme(theme) {
  document.documentElement.setAttribute("data-theme", theme);
  localStorage.setItem("theme", theme);
  document.getElementById("btn-theme").textContent = theme === "dark" ? "🌙" : "☀️";
}

applyTheme(getPreferredTheme());

document.getElementById("btn-theme").addEventListener("click", () => {
  const current = document.documentElement.getAttribute("data-theme") || "dark";
  applyTheme(current === "dark" ? "light" : "dark");
});

function isDark() {
  return document.documentElement.getAttribute("data-theme") !== "light";
}

// --- API ---

async function api(path, opts = {}) {
  const res = await fetch(`/api${path}`, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  if (!res.ok && res.status !== 204) {
    const text = await res.text();
    throw new Error(text || res.statusText);
  }
  if (res.status === 204) return null;
  return res.json();
}

const API = {
  listCards: () => api("/cards"),
  createCard: (data) => api("/cards", { method: "POST", body: JSON.stringify(data) }),
  jumpCard: (id) => api(`/cards/${id}/jump`, { method: "POST" }),
  deleteCard: (id) => api(`/cards/${id}`, { method: "DELETE" }),
  moveCard: (id, column) => api(`/cards/${id}/move`, { method: "POST", body: JSON.stringify({ column }) }),
  diffCard: (id) => api(`/cards/${id}/diff`),
  openVSCode: (id) => api(`/cards/${id}/vscode`, { method: "POST" }),
  listProjects: () => api("/projects"),
  createProject: (data) => api("/projects", { method: "POST", body: JSON.stringify(data) }),
  deleteProject: (id) => api(`/projects/${id}`, { method: "DELETE" }),
  clearColumn: (column) => api(`/columns/${column}/clear`, { method: "POST" }),
  listColumns: () => api("/columns"),
  updateColumns: (data) => api("/columns", { method: "PUT", body: JSON.stringify(data) }),
};

// --- State ---

let cards = [];
let projects = [];
let columns = [];
let draggedCard = null;

async function refresh() {
  [cards, projects, columns] = await Promise.all([
    API.listCards(),
    API.listProjects(),
    API.listColumns(),
  ]);
  renderBoard();
}

// --- SSE ---

function connectSSE() {
  const src = new EventSource("/api/events");
  src.onmessage = () => refresh();
  src.onerror = () => {
    // Close the failed source before reconnecting, otherwise its built-in
    // retry piles up connections alongside the new one.
    src.close();
    setTimeout(connectSSE, 2000);
  };
}

// --- Render ---

// Interpolate a color from orange (#e3873d) to green (#3fb950) based on t in [0,1].
function columnColor(index, total) {
  const t = total <= 1 ? 1 : index / (total - 1);
  const r = Math.round(227 + (63 - 227) * t);
  const g = Math.round(135 + (185 - 135) * t);
  const b = Math.round(61 + (80 - 61) * t);
  return `rgb(${r}, ${g}, ${b})`;
}

function isForwardMove(srcColId, dstColId) {
  if (srcColId === dstColId) return false;
  const srcIdx = columns.findIndex((c) => c.id === srcColId);
  const dstIdx = columns.findIndex((c) => c.id === dstColId);
  return dstIdx > srcIdx;
}

function renderBoard() {
  const board = document.getElementById("board");
  board.innerHTML = "";

  for (let i = 0; i < columns.length; i++) {
    const col = columns[i];
    const color = columnColor(i, columns.length);
    const colCards = cards.filter((c) => c.column === col.id);

    const isLastCol = i === columns.length - 1;
    const headerExtra = i === 0 ? `<button class="btn-add-task" title="New task">+</button>` : "";
    const clearExtra = isLastCol && colCards.length > 0 ? `<button class="btn-clear-column" title="Clear all cards">🗑</button>` : "";

    const colEl = document.createElement("div");
    colEl.className = "column";
    colEl.style.setProperty("--col-accent", color);
    colEl.innerHTML = `
      <div class="column-header">
        <span class="column-title">${col.emoji} ${esc(col.name)}</span>
        <div class="column-header-actions">
          ${headerExtra}
          ${clearExtra}
          <span class="card-count">${colCards.length}</span>
        </div>
      </div>
      <div class="column-body" data-column="${col.id}"></div>
    `;

    const body = colEl.querySelector(".column-body");

    // Drop zone handlers
    body.addEventListener("dragover", (e) => {
      e.preventDefault();
      if (draggedCard?.status === "running" && isForwardMove(draggedCard.column, col.id)) {
        e.dataTransfer.dropEffect = "none";
        return;
      }
      e.dataTransfer.dropEffect = "move";
      body.classList.add("drop-target");
    });

    body.addEventListener("dragleave", (e) => {
      if (!body.contains(e.relatedTarget)) {
        body.classList.remove("drop-target");
      }
    });

    body.addEventListener("drop", async (e) => {
      e.preventDefault();
      body.classList.remove("drop-target");
      const cardId = e.dataTransfer.getData("text/plain");
      if (!cardId) return;
      if (draggedCard?.status === "running" && isForwardMove(draggedCard.column, col.id)) return;
      try {
        await API.moveCard(cardId, col.id);
      } catch (err) {
        alert(err.message);
      }
    });

    if (colCards.length === 0) {
      body.innerHTML = `<div class="empty-column">No tasks</div>`;
    } else {
      for (const card of colCards) {
        body.appendChild(renderCard(card, col.id));
      }
    }

    const addBtn = colEl.querySelector(".btn-add-task");
    if (addBtn) {
      addBtn.addEventListener("click", openNewTaskDialog);
    }

    const clearBtn = colEl.querySelector(".btn-clear-column");
    if (clearBtn) {
      clearBtn.addEventListener("click", async () => {
        const count = colCards.length;
        if (count === 0) return;
        if (!confirm(`Delete all ${count} card${count !== 1 ? "s" : ""} and their worktrees?`)) return;
        try {
          await API.clearColumn(col.id);
        } catch (err) {
          alert(err.message);
        }
      });
    }

    board.appendChild(colEl);
  }
}

function renderCard(card, colId) {
  const el = document.createElement("div");
  el.className = `card card-${card.status}`;
  el.dataset.cardId = card.id;

  el.draggable = true;

  const isLastCol = columns.length > 0 && columns[columns.length - 1].id === colId;

  el.innerHTML = `
    <div class="card-title">${esc(card.title)}</div>
    <div class="card-actions">
      <button class="btn btn-small btn-secondary" data-action="jump" data-id="${card.id}" data-session="${esc(card.session)}" title="Open agent session">Agent</button>
      <button class="btn btn-small btn-secondary" data-action="diff" data-id="${card.id}" title="View worktree diff">Diff</button>
      <button class="btn btn-small btn-secondary" data-action="vscode" data-id="${card.id}" title="Open in VSCode">Code</button>
      <button class="btn btn-small btn-secondary btn-delete" data-action="delete" data-id="${card.id}" title="Delete task and worktree">✕</button>
    </div>
  `;

  el.addEventListener("click", handleCardAction);

  el.addEventListener("dragstart", (e) => {
    draggedCard = card;
    e.dataTransfer.setData("text/plain", card.id);
    e.dataTransfer.effectAllowed = "move";
    el.classList.add("dragging");
  });

  el.addEventListener("dragend", () => {
    draggedCard = null;
    el.classList.remove("dragging");
  });

  return el;
}

async function handleCardAction(e) {
  const btn = e.target.closest("[data-action]");
  if (!btn) return;

  const { action, id } = btn.dataset;

  try {
    if (action === "jump") {
      const info = await API.jumpCard(id);
      openTerminal(info.session, cards.find((c) => c.id === id)?.title || "Terminal");
    } else if (action === "diff") {
      const title = cards.find((c) => c.id === id)?.title || "Diff";
      openDiffDialog(id, title);
    } else if (action === "vscode") {
      await API.openVSCode(id);
    } else if (action === "delete") {
      if (confirm("Delete this card and its worktree?")) {
        await API.deleteCard(id);
      }
    }
  } catch (err) {
    alert(err.message);
  }
}

// --- Terminal ---

let activeTerm = null;
let activeSocket = null;

async function openTerminal(sessionName, title) {
  const dialog = document.getElementById("terminal-dialog");
  const container = document.getElementById("terminal-container");
  document.getElementById("terminal-title").textContent = title;

  closeTerminal();
  dialog.showModal();

  await ghosttyReady;

  const term = new GhosttyTerminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', Menlo, monospace",
    theme: isDark()
      ? {
          background: "#0d1117",
          foreground: "#e6edf3",
          cursor: "#58a6ff",
          selectionBackground: "#264f78",
          black: "#0d1117",
          red: "#f85149",
          green: "#3fb950",
          yellow: "#d29922",
          blue: "#58a6ff",
          magenta: "#bc8cff",
          cyan: "#39c5cf",
          white: "#b1bac4",
        }
      : {
          background: "#ffffff",
          foreground: "#1f2328",
          cursor: "#0969da",
          selectionBackground: "#b6d5f5",
          black: "#1f2328",
          red: "#cf222e",
          green: "#1a7f37",
          yellow: "#9a6700",
          blue: "#0969da",
          magenta: "#8250df",
          cyan: "#1b7c83",
          white: "#f6f8fa",
        },
  });

  const fitAddon = new FitAddon();
  term.loadAddon(fitAddon);
  term.open(container);
  activeTerm = term;

  // PR #136 fix: forward wheel events with coordinates when mouse tracking
  // is active, so tmux panes and other TUI split views scroll correctly.
  term.attachCustomWheelEventHandler((e) => {
    if (term.hasMouseTracking()) {
      term.inputHandler?.handleWheel(e);
      return true;
    }
    return false;
  });

  requestAnimationFrame(() => {
    fitAddon.fit();

    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${protocol}//${location.host}/api/terminal/${sessionName}?cols=${term.cols}&rows=${term.rows}`;
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    activeSocket = socket;

    socket.onmessage = (e) => term.write(new Uint8Array(e.data));
    socket.onclose = () => term.write("\r\n\x1b[90m[session ended]\x1b[0m\r\n");
    socket.onerror = () => term.write("\r\n\x1b[31m[connection error]\x1b[0m\r\n");

    term.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(data);
    });
    term.onResize(({ cols, rows }) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "resize", cols, rows }));
    });
  });

  const resizeHandler = () => {
    if (activeTerm) fitAddon.fit();
  };
  window.addEventListener("resize", resizeHandler);
  dialog._resizeHandler = resizeHandler;
}

function closeTerminal() {
  const dialog = document.getElementById("terminal-dialog");
  const container = document.getElementById("terminal-container");

  if (activeSocket) {
    activeSocket.close();
    activeSocket = null;
  }
  if (activeTerm) {
    activeTerm.dispose();
    activeTerm = null;
  }

  container.innerHTML = "";

  if (dialog._resizeHandler) {
    window.removeEventListener("resize", dialog._resizeHandler);
    dialog._resizeHandler = null;
  }
}

document.getElementById("close-terminal").addEventListener("click", () => {
  document.getElementById("terminal-dialog").close();
});

// The terminal dialog hosts a live TUI: ESC must reach the agent (so menus,
// modes, etc. behave normally) but neither dismiss the <dialog> nor flow
// through ghostty-web's keydown handler, which encodes ESC using the Kitty
// keyboard protocol (e.g. `\x1b[27u`) and shows up as garbage in bubbletea
// TUIs. We send a plain `\x1b` byte ourselves and stop the event there.
document.getElementById("terminal-dialog").addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  e.preventDefault();
  e.stopPropagation();
  if (activeSocket && activeSocket.readyState === WebSocket.OPEN) {
    activeSocket.send("\x1b");
  }
}, true);

// Belt and suspenders: if the browser still tries to dismiss the dialog on
// ESC (e.g. before our keydown listener runs), keep it open.
document.getElementById("terminal-dialog").addEventListener("cancel", (e) => {
  e.preventDefault();
});

document.getElementById("terminal-dialog").addEventListener("close", closeTerminal);

// Generic close button for all dialogs
document.querySelectorAll(".dialog-close[data-close]").forEach((btn) => {
  btn.addEventListener("click", () => {
    btn.closest("dialog").close();
  });
});

// --- Dialogs ---

// New task dialog
async function openNewTaskDialog() {
  const select = document.getElementById("task-project");
  select.innerHTML = `<option value="">Default (cagent/main)</option>`;
  for (const p of projects) {
    select.innerHTML += `<option value="${p.id}">${esc(p.name)}</option>`;
  }
  document.getElementById("new-task-dialog").showModal();
  document.getElementById("task-prompt").focus();
}

document.getElementById("new-task-dialog").querySelector("form").addEventListener("submit", submitNewTask);

document.getElementById("new-task-dialog").addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
    e.preventDefault();
    document.getElementById("new-task-dialog").querySelector("form").requestSubmit();
  }
});

async function submitNewTask(e) {
  e.preventDefault();
  const prompt = document.getElementById("task-prompt").value.trim();
  const projectId = document.getElementById("task-project").value;

  if (!prompt) return;

  const dialog = document.getElementById("new-task-dialog");
  const submitBtn = dialog.querySelector("button[type=submit]");
  const originalText = submitBtn.textContent;
  submitBtn.disabled = true;
  submitBtn.classList.add("btn-loading");
  submitBtn.textContent = "Creating…";

  try {
    await API.createCard({ prompt, projectId });
    dialog.close();
    document.getElementById("task-prompt").value = "";
  } catch (err) {
    alert(err.message);
    submitBtn.disabled = false;
    submitBtn.classList.remove("btn-loading");
    submitBtn.textContent = originalText;
  }
}

document.getElementById("new-task-dialog").addEventListener("close", () => {
  const submitBtn = document.getElementById("new-task-dialog").querySelector("button[type=submit]");
  submitBtn.disabled = false;
  submitBtn.classList.remove("btn-loading");
  submitBtn.textContent = "🤖 Create Task";
});

// Projects dialog
document.getElementById("btn-projects").addEventListener("click", async () => {
  renderProjects();
  document.getElementById("projects-dialog").showModal();
});

document.getElementById("projects-dialog").querySelector("form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const name = document.getElementById("proj-name").value.trim();
  const repoPath = document.getElementById("proj-repo").value.trim();
  const agent = document.getElementById("proj-agent").value.trim();

  if (!name) return;

  try {
    await API.createProject({ name, repoPath, agent });
    document.getElementById("proj-name").value = "";
    document.getElementById("proj-repo").value = "";
    document.getElementById("proj-agent").value = "";
    await refresh();
    renderProjects();
  } catch (err) {
    alert(err.message);
  }
});

function renderProjects() {
  const list = document.getElementById("projects-list");
  if (projects.length === 0) {
    list.innerHTML = `<div class="empty-column">No projects yet</div>`;
    return;
  }
  list.innerHTML = projects.map((p) => `
    <div class="project-item">
      <div>
        <div class="project-name">${esc(p.name)}</div>
        <div class="project-path">${esc(p.repoPath)}</div>
      </div>
      <button class="btn btn-small btn-danger" onclick="deleteProject('${p.id}')" title="Delete project">✕</button>
    </div>
  `).join("");
}

window.deleteProject = async (id) => {
  await API.deleteProject(id);
  await refresh();
  renderProjects();
};

// Columns dialog
document.getElementById("btn-columns").addEventListener("click", () => {
  renderColumnsEditor();
  document.getElementById("columns-dialog").showModal();
});

document.getElementById("columns-dialog").querySelector("form").addEventListener("submit", async (e) => {
  e.preventDefault();

  const updates = columns.map((col) => {
    const textarea = document.getElementById(`col-prompt-${col.id}`);
    return { id: col.id, prompt: textarea ? textarea.value : col.prompt };
  });

  try {
    await API.updateColumns(updates);
    document.getElementById("columns-dialog").close();
  } catch (err) {
    alert(err.message);
  }
});

function renderColumnsEditor() {
  const list = document.getElementById("columns-list");
  list.innerHTML = columns.map((col) => `
    <div class="column-prompt-item">
      <div class="column-prompt-name">${esc(col.name)}</div>
      <textarea id="col-prompt-${col.id}" rows="3" placeholder="No prompt (manual column)" autocomplete="off" autocorrect="off" autocapitalize="off" spellcheck="false">${esc(col.prompt)}</textarea>
    </div>
  `).join("");
}

// --- Diff Dialog ---

// Files larger than this stay collapsed by default to avoid huge initial layouts.
const DIFF_AUTO_COLLAPSE_LINES = 500;
// Hard cap on rendered rows per file. Browsers struggle with millions of <tr>s
// (e.g. checked-in lockfiles); a truncation notice is appended when this hits.
const DIFF_MAX_LINES_PER_FILE = 5000;
// Number of files rendered per animation frame, so the UI stays responsive on
// huge diffs.
const DIFF_FILES_PER_FRAME = 5;

let diffRenderToken = 0;

async function openDiffDialog(cardId, title) {
  const dialog = document.getElementById("diff-dialog");
  const container = document.getElementById("diff-container");
  document.getElementById("diff-title").textContent = `📄 ${title}`;
  container.innerHTML = `<div class="diff-loading">Loading diff…</div>`;
  dialog.showModal();

  const token = ++diffRenderToken;

  try {
    const data = await API.diffCard(cardId);
    if (token !== diffRenderToken) return;
    const diff = data.diff || "";
    if (!diff.trim()) {
      container.innerHTML = `<div class="diff-empty">No changes</div>`;
      return;
    }
    renderDiffInto(container, diff, token);
  } catch (err) {
    if (token !== diffRenderToken) return;
    container.innerHTML = `<div class="diff-empty">Error: ${esc(err.message)}</div>`;
  }
}

function renderDiffInto(container, rawDiff, token) {
  const files = parseDiffFiles(rawDiff);
  if (files.length === 0) {
    container.innerHTML = `<div class="diff-empty">No changes</div>`;
    return;
  }

  container.innerHTML = renderDiffStats(files);

  // Render files incrementally in batches so the main thread stays free.
  let i = 0;
  const renderBatch = () => {
    if (token !== diffRenderToken) return;
    const end = Math.min(i + DIFF_FILES_PER_FRAME, files.length);
    let html = "";
    for (; i < end; i++) {
      html += renderDiffFile(files[i]);
    }
    container.insertAdjacentHTML("beforeend", html);
    if (i < files.length) {
      requestAnimationFrame(renderBatch);
    }
  };
  requestAnimationFrame(renderBatch);
}

function renderDiffFile(file) {
  let added = 0;
  let removed = 0;
  let lineCount = 0;
  for (const hunk of file.hunks) {
    for (const line of hunk.lines) {
      lineCount++;
      if (line.type === "+") added++;
      else if (line.type === "-") removed++;
    }
  }

  let rendered = 0;
  let truncated = false;
  let linesHtml = "";
  for (const hunk of file.hunks) {
    if (rendered >= DIFF_MAX_LINES_PER_FILE) {
      truncated = true;
      break;
    }
    linesHtml += `<tr class="diff-hunk-header"><td colspan="3">${esc(hunk.header)}</td></tr>`;
    for (const line of hunk.lines) {
      if (rendered >= DIFF_MAX_LINES_PER_FILE) {
        truncated = true;
        break;
      }
      const cls = line.type === "+" ? "diff-add" : line.type === "-" ? "diff-del" : "diff-ctx";
      const oldNum = line.oldNum ?? "";
      const newNum = line.newNum ?? "";
      linesHtml += `<tr class="${cls}"><td class="diff-ln">${oldNum}</td><td class="diff-ln">${newNum}</td><td class="diff-code">${esc(line.text)}</td></tr>`;
      rendered++;
    }
  }
  if (truncated) {
    const remaining = lineCount - rendered;
    linesHtml += `<tr class="diff-hunk-header"><td colspan="3">… ${remaining} more line${remaining !== 1 ? "s" : ""} not shown</td></tr>`;
  }

  const badge = `<span class="diff-file-adds">+${added}</span> <span class="diff-file-dels">-${removed}</span>`;
  const openAttr = lineCount > DIFF_AUTO_COLLAPSE_LINES ? "" : " open";

  return `
    <details class="diff-file"${openAttr}>
      <summary class="diff-file-header">
        <span class="diff-file-name">${esc(file.name)}</span>
        <span class="diff-file-stats">${badge}</span>
      </summary>
      <table class="diff-table">${linesHtml}</table>
    </details>
  `;
}

function renderDiffStats(files) {
  let totalAdded = 0;
  let totalRemoved = 0;
  for (const file of files) {
    for (const hunk of file.hunks) {
      for (const line of hunk.lines) {
        if (line.type === "+") totalAdded++;
        if (line.type === "-") totalRemoved++;
      }
    }
  }
  return `
    <div class="diff-stats">
      <span>${files.length} file${files.length !== 1 ? "s" : ""} changed</span>
      <span class="diff-file-adds">+${totalAdded}</span>
      <span class="diff-file-dels">-${totalRemoved}</span>
    </div>
  `;
}

function parseDiffFiles(raw) {
  const files = [];
  const fileChunks = raw.split(/^diff --git /m).filter(Boolean);

  for (const chunk of fileChunks) {
    const lines = chunk.split("\n");
    // Extract file name from "a/... b/..."
    const firstLine = lines[0] || "";
    const match = firstLine.match(/b\/(.+)$/);
    const name = match ? match[1] : firstLine;

    const hunks = [];
    let currentHunk = null;
    let oldLine = 0;
    let newLine = 0;

    for (const line of lines.slice(1)) {
      if (line.startsWith("@@")) {
        const hunkMatch = line.match(/@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)/);
        oldLine = hunkMatch ? parseInt(hunkMatch[1], 10) : 0;
        newLine = hunkMatch ? parseInt(hunkMatch[2], 10) : 0;
        currentHunk = { header: line, lines: [] };
        hunks.push(currentHunk);
      } else if (currentHunk) {
        if (line.startsWith("+")) {
          currentHunk.lines.push({ type: "+", text: line.slice(1), newNum: newLine++ });
        } else if (line.startsWith("-")) {
          currentHunk.lines.push({ type: "-", text: line.slice(1), oldNum: oldLine++ });
        } else if (line.startsWith(" ")) {
          currentHunk.lines.push({ type: " ", text: line.slice(1), oldNum: oldLine++, newNum: newLine++ });
        }
      }
    }

    if (hunks.length > 0) {
      files.push({ name, hunks });
    }
  }

  return files;
}

document.getElementById("close-diff").addEventListener("click", () => {
  document.getElementById("diff-dialog").close();
});

document.getElementById("diff-dialog").addEventListener("close", () => {
  diffRenderToken++;
  document.getElementById("diff-container").innerHTML = "";
});

document.getElementById("diff-dialog").addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    document.getElementById("diff-dialog").close();
  }
});

document.getElementById("diff-dialog").addEventListener("click", (e) => {
  if (e.target === e.currentTarget) {
    document.getElementById("diff-dialog").close();
  }
});

// --- Utils ---

function esc(s) {
  const el = document.createElement("span");
  el.textContent = s || "";
  return el.innerHTML;
}

// --- Keyboard shortcuts ---

document.addEventListener("keydown", (e) => {
  if (e.key !== "n" && e.key !== "N") return;
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (e.target.matches("input, textarea, select")) return;
  if (document.querySelector("dialog[open]")) return;

  e.preventDefault();
  openNewTaskDialog();
});

// --- Init ---

refresh();
connectSSE();
