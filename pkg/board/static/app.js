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
  prStatus: (id) => api(`/cards/${id}/pr`),
  openVSCode: (id) => api(`/cards/${id}/vscode`, { method: "POST" }),
  listProjects: () => api("/projects"),
  createProject: (data) => api("/projects", { method: "POST", body: JSON.stringify(data) }),
  deleteProject: (id) => api(`/projects/${id}`, { method: "DELETE" }),
  reorderProjects: (ids) => api("/projects/order", { method: "PUT", body: JSON.stringify(ids) }),
  listAgents: () => api("/agents"),
  browse: (path) => api(`/browse${path ? `?path=${encodeURIComponent(path)}` : ""}`),
  clearColumn: (column) => api(`/columns/${encodeURIComponent(column)}/clear`, { method: "POST" }),
  listColumns: () => api("/columns"),
  updateColumns: (data) => api("/columns", { method: "PUT", body: JSON.stringify(data) }),
};

// --- State ---

let cards = [];
let projects = [];
let columns = [];
let draggedCard = null;
let homeDir = "";

async function refresh() {
  [cards, projects, columns] = await Promise.all([
    API.listCards(),
    API.listProjects(),
    API.listColumns(),
  ]);
  renderBoard();
}

// Replace the home directory prefix with "~" for display.
function shortenPath(p) {
  if (homeDir && (p === homeDir || p.startsWith(homeDir + "/"))) {
    return "~" + p.slice(homeDir.length);
  }
  return p;
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

// --- Two-step buttons ---

// armButton implements two-step (two-click) confirmation: the first click
// arms the button by appending " ?" to its label and returns false; the
// second click returns true so the caller performs the action. Losing focus
// disarms the button, and any re-render replaces it, which also disarms.
function armButton(btn) {
  if (btn.dataset.armed) {
    disarmButton(btn);
    return true;
  }
  btn.dataset.armed = "true";
  btn.textContent += " ?";
  btn.addEventListener("blur", () => disarmButton(btn), { once: true });
  return false;
}

function disarmButton(btn) {
  if (!btn.dataset.armed) return;
  delete btn.dataset.armed;
  btn.textContent = btn.textContent.replace(/ \?$/, "");
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

// A busy agent (starting or mid-turn) cannot accept a prompt yet.
function isBusy(card) {
  return card?.status === "starting" || card?.status === "running";
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
        <span class="column-title">${esc(col.emoji)} ${esc(col.name)}</span>
        <div class="column-header-actions">
          ${headerExtra}
          ${clearExtra}
          <span class="card-count">${colCards.length}</span>
        </div>
      </div>
      <div class="column-body" data-column="${esc(col.id)}"></div>
    `;

    const body = colEl.querySelector(".column-body");

    // Drop zone handlers
    body.addEventListener("dragover", (e) => {
      e.preventDefault();
      if (isBusy(draggedCard) && isForwardMove(draggedCard.column, col.id)) {
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
      if (isBusy(draggedCard) && isForwardMove(draggedCard.column, col.id)) return;
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
        if (colCards.length === 0) return;
        if (!armButton(clearBtn)) return;
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

// prIcon renders an Octicon SVG (16px viewBox) for a PR status, tinted via
// currentColor by the matching .card-pr-<status> CSS class. The paths are
// GitHub's own Primer octicons: a distinct glyph per state so "ready to merge"
// and "merged" (and draft/closed) are told apart at a glance.
const PR_ICON_PATHS = {
  // git-pull-request: open, waiting for review (default before status loads).
  open: `<path d="M1.5 3.25a2.25 2.25 0 1 1 3 2.122v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.25 2.25 0 0 1 1.5 3.25Zm5.677-.177L9.573.677A.25.25 0 0 1 10 .854V2.5h1A2.5 2.5 0 0 1 13.5 5v5.628a2.251 2.251 0 1 1-1.5 0V5a1 1 0 0 0-1-1h-1v1.646a.25.25 0 0 1-.427.177L7.177 3.427a.25.25 0 0 1 0-.354ZM3.75 2.5a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm0 9.5a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm8.25.75a.75.75 0 1 0 1.5 0 .75.75 0 0 0-1.5 0Z"/>`,
  // git-pull-request-draft.
  draft: `<path d="M3.25 1A2.25 2.25 0 0 1 4 5.372v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.251 2.251 0 0 1 3.25 1Zm9.5 14a2.25 2.25 0 1 1 0-4.5 2.25 2.25 0 0 1 0 4.5ZM2.5 3.25a.75.75 0 1 0 1.5 0 .75.75 0 0 0-1.5 0ZM3.25 12a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm9.5 0a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5ZM14 7.5a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0Zm0-4.25a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0Z"/>`,
  // git-pull-request-closed.
  closed: `<path d="M3.25 1A2.25 2.25 0 0 1 4 5.372v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.251 2.251 0 0 1 3.25 1Zm9.5 5.5a.75.75 0 0 1 .75.75v3.378a2.251 2.251 0 1 1-1.5 0V7.25a.75.75 0 0 1 .75-.75Zm-2.03-5.273a.75.75 0 0 1 1.06 0l.97.97.97-.97a.748.748 0 0 1 1.265.332.75.75 0 0 1-.205.729l-.97.97.97.97a.751.751 0 0 1-.018 1.042.751.751 0 0 1-1.042.018l-.97-.97-.97.97a.749.749 0 0 1-1.275-.326.749.749 0 0 1 .215-.734l.97-.97-.97-.97a.75.75 0 0 1 0-1.06ZM2.5 3.25a.75.75 0 1 0 1.5 0 .75.75 0 0 0-1.5 0ZM3.25 12a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm9.5 0a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Z"/>`,
  // git-merge.
  merged: `<path d="M5.45 5.154A4.25 4.25 0 0 0 9.25 7.5h1.378a2.251 2.251 0 1 1 0 1.5H9.25A5.734 5.734 0 0 1 5 7.123v3.505a2.25 2.25 0 1 1-1.5 0V5.372a2.25 2.25 0 1 1 1.95-.218ZM4.25 13.5a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5Zm8.5-4.5a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5ZM5 3.25a.75.75 0 1 0 0 .005V3.25Z"/>`,
  // check-circle-fill: green (CI passed / approved).
  success: `<path d="M8 16A8 8 0 1 1 8 0a8 8 0 0 1 0 16Zm3.78-9.72a.751.751 0 0 0-.018-1.042.751.751 0 0 0-1.042-.018L6.75 9.19 5.28 7.72a.751.751 0 0 0-1.042.018.751.751 0 0 0-.018 1.042l2 2a.75.75 0 0 0 1.06 0Z"/>`,
  // x-circle-fill: red (CI failed / changes requested).
  failure: `<path d="M2.343 13.657A8 8 0 1 1 13.658 2.343 8 8 0 0 1 2.343 13.657ZM6.03 4.97a.751.751 0 0 0-1.042.018.751.751 0 0 0-.018 1.042L6.94 8 4.97 9.97a.749.749 0 0 0 .326 1.275.749.749 0 0 0 .734-.215L8 9.06l1.97 1.97a.749.749 0 0 0 1.275-.326.749.749 0 0 0-.215-.734L9.06 8l1.97-1.97a.749.749 0 0 0-.326-1.275.749.749 0 0 0-.734.215L8 6.94Z"/>`,
  // hourglass: yellow (CI running).
  pending: `<path d="M2.75 1h10.5a.75.75 0 0 1 0 1.5h-.75v1.25a4.75 4.75 0 0 1-1.9 3.8l-.333.25a.25.25 0 0 0 0 .4l.333.25a4.75 4.75 0 0 1 1.9 3.8v1.25h.75a.75.75 0 0 1 0 1.5H2.75a.75.75 0 0 1 0-1.5h.75v-1.25a4.75 4.75 0 0 1 1.9-3.8l.333-.25a.25.25 0 0 0 0-.4L5.4 7.55a4.75 4.75 0 0 1-1.9-3.8V2.5h-.75a.75.75 0 0 1 0-1.5ZM11 2.5H5v1.25c0 1.023.482 1.986 1.3 2.6l.333.25c.934.7.934 2.1 0 2.8l-.333.25a3.251 3.251 0 0 0-1.3 2.6v1.25h6v-1.25a3.251 3.251 0 0 0-1.3-2.6l-.333-.25a1.748 1.748 0 0 1 0-2.8l.333-.25a3.251 3.251 0 0 0 1.3-2.6Z"/>`,
};

// prIconSvg wraps a status's octicon path in an <svg>. Unknown statuses fall
// back to the plain open-PR glyph.
function prIconSvg(status) {
  const path = PR_ICON_PATHS[status] || PR_ICON_PATHS.open;
  return `<svg class="card-pr-icon" viewBox="0 0 16 16" width="13" height="13" aria-hidden="true" fill="currentColor">${path}</svg>`;
}

// PR_STATUS_LABEL maps a PR status to a human-readable tooltip suffix.
const PR_STATUS_LABEL = {
  open: "Waiting for review",
  draft: "Draft",
  closed: "Closed",
  merged: "Merged",
  success: "Ready to merge",
  failure: "Changes needed",
  pending: "CI running",
};

// PR_APPROVED_SVG is the Octicon check mark shown, in green, when a PR has an
// approving review and no outstanding change requests. It is separate from the
// status icon so approval reads independently of CI/merge state.
const PR_APPROVED_SVG = `<svg class="card-pr-approved" viewBox="0 0 16 16" width="13" height="13" aria-hidden="true" fill="currentColor"><path d="M13.78 4.22a.75.75 0 0 1 0 1.06l-7.25 7.25a.75.75 0 0 1-1.06 0L2.22 9.28a.751.751 0 0 1 .018-1.042.751.751 0 0 1 1.042-.018L6 10.94l6.72-6.72a.75.75 0 0 1 1.06 0Z"/></svg>`;

function renderCard(card, colId) {
  const el = document.createElement("div");
  el.className = `card card-${card.status}`;
  el.dataset.cardId = card.id;

  el.draggable = true;

  const isLastCol = columns.length > 0 && columns[columns.length - 1].id === colId;

  const cost = formatCost(card.cost);
  const costHtml = cost ? `<span class="card-cost" title="Total agent cost">${cost}</span>` : "";

  // Show a link to the pull request once the Push column has opened one. The
  // GitHub-mark icon is uncolored initially; its merge/CI status is fetched
  // per card after render (see loadPRStatus) and applied as a modifier class.
  const prLink = card.prUrl
    ? `<a class="card-pr" href="${esc(card.prUrl)}" target="_blank" rel="noopener noreferrer" title="${esc(card.prUrl)}">${prIconSvg("open")}<span class="card-pr-label">${esc(prLabel(card.prUrl))}</span></a>`
    : "";

  // Cost and PR link share a single meta row; the wrapper is omitted entirely
  // when neither is present so an empty line never adds card height.
  const metaHtml = costHtml || prLink
    ? `<div class="card-meta">${costHtml}${prLink}</div>`
    : "";

  el.innerHTML = `
    <div class="card-title">${esc(card.title)}</div>
    ${metaHtml}
    <div class="card-actions">
      <button class="btn btn-small btn-secondary" data-action="jump" data-id="${card.id}" title="Open agent session">Agent</button>
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

  // Load the PR's merge/CI status on render (no auto-refresh): a card with a
  // PR link gets its GitHub icon colored once the status comes back.
  if (card.prUrl) {
    loadPRStatus(card.id, el.querySelector(".card-pr"));
  }

  return el;
}

// loadPRStatus fetches a card's pull request status, then swaps the link's
// icon to the matching octicon, applies a color class, and appends a green
// check mark when the PR is approved. It is best-effort: any error, or an
// empty status, leaves the default (open-PR) icon in place. The link element
// may be detached by a re-render before the fetch resolves; the update is
// applied to whatever element was passed, which is then discarded harmlessly
// if stale.
async function loadPRStatus(id, linkEl) {
  if (!linkEl) return;
  let status, approved;
  try {
    ({ status, approved } = await API.prStatus(id));
  } catch {
    return; // best-effort: leave the default icon
  }
  if (!status) return;
  linkEl.classList.add(`card-pr-${status}`);
  const icon = linkEl.querySelector(".card-pr-icon");
  if (icon) icon.outerHTML = prIconSvg(status);
  let title = `${linkEl.title} — ${PR_STATUS_LABEL[status] || status}`;
  // A green check mark for an approved PR, shown independently of the CI/merge
  // status icon. Skipped once merged: approval is implied and adds no signal.
  if (approved && status !== "merged") {
    linkEl.insertAdjacentHTML("beforeend", PR_APPROVED_SVG);
    title += " — Approved";
  }
  linkEl.title = title;
}

async function handleCardAction(e) {
  const btn = e.target.closest("[data-action]");
  if (!btn) return;

  const { action, id } = btn.dataset;

  try {
    if (action === "jump") {
      let info;
      try {
        info = await API.jumpCard(id);
      } catch (err) {
        // 503 while the docker agent is still coming up: tell the user instead
        // of attaching a terminal to the bare launch command.
        alert("The agent is still starting. Try again in a moment.");
        return;
      }
      const card = cards.find((c) => c.id === id);
      openTerminal(info.session, card?.title || "Terminal", id, card?.project || "");
    } else if (action === "diff") {
      const title = cards.find((c) => c.id === id)?.title || "Diff";
      openDiffDialog(id, title);
    } else if (action === "vscode") {
      await API.openVSCode(id);
    } else if (action === "delete") {
      if (!armButton(btn)) return;
      await API.deleteCard(id);
    }
  } catch (err) {
    alert(err.message);
  }
}

// --- Terminal ---

let activeTerm = null;
let activeSocket = null;
let activeCardId = null;

async function openTerminal(sessionName, title, cardId, project) {
  const dialog = document.getElementById("terminal-dialog");
  const container = document.getElementById("terminal-container");
  document.getElementById("terminal-title").textContent = title;
  document.getElementById("terminal-project").textContent = project || "";
  activeCardId = cardId;

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

// Diff/Code buttons in the terminal header act on the current card.
document.getElementById("terminal-diff").addEventListener("click", () => {
  if (!activeCardId) return;
  const title = cards.find((c) => c.id === activeCardId)?.title || "Diff";
  openDiffDialog(activeCardId, title);
});

document.getElementById("terminal-vscode").addEventListener("click", async () => {
  if (!activeCardId) return;
  try {
    await API.openVSCode(activeCardId);
  } catch (err) {
    alert(err.message);
  }
});

// Copy the card's full text to the clipboard: the full title once the agent
// has generated one, otherwise the full prompt (the placeholder title is a
// truncated version of it). GitHub-style icons: copy at rest, a green check
// as brief confirmation.
const COPY_ICON_SVG = `<svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor" aria-hidden="true"><path d="M0 6.75C0 5.784.784 5 1.75 5h1.5a.75.75 0 0 1 0 1.5h-1.5a.25.25 0 0 0-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 0 0 .25-.25v-1.5a.75.75 0 0 1 1.5 0v1.5A1.75 1.75 0 0 1 9.25 16h-7.5A1.75 1.75 0 0 1 0 14.25Z"></path><path d="M5 1.75C5 .784 5.784 0 6.75 0h7.5C15.216 0 16 .784 16 1.75v7.5A1.75 1.75 0 0 1 14.25 11h-7.5A1.75 1.75 0 0 1 5 9.25Zm1.75-.25a.25.25 0 0 0-.25.25v7.5c0 .138.112.25.25.25h7.5a.25.25 0 0 0 .25-.25v-7.5a.25.25 0 0 0-.25-.25Z"></path></svg>`;
const CHECK_ICON_SVG = `<svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor" aria-hidden="true"><path d="M13.78 4.22a.75.75 0 0 1 0 1.06l-7.25 7.25a.75.75 0 0 1-1.06 0L2.22 9.28a.751.751 0 0 1 .018-1.042.751.751 0 0 1 1.042-.018L6 10.94l6.72-6.72a.75.75 0 0 1 1.06 0Z"></path></svg>`;

const copyTitleBtn = document.getElementById("terminal-copy-title");
copyTitleBtn.innerHTML = COPY_ICON_SVG;

copyTitleBtn.addEventListener("click", async () => {
  const card = cards.find((c) => c.id === activeCardId);
  // Prefer the full prompt while the title is still the truncated
  // placeholder; fall back to the displayed title for cards predating the
  // prompt column.
  const text =
    (card && (card.titleGenerated ? card.title : card.prompt || card.title)) ||
    document.getElementById("terminal-title").textContent;
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    return; // clipboard unavailable (e.g. insecure context)
  }
  copyTitleBtn.innerHTML = CHECK_ICON_SVG;
  copyTitleBtn.classList.add("copied");
  setTimeout(() => {
    copyTitleBtn.innerHTML = COPY_ICON_SVG;
    copyTitleBtn.classList.remove("copied");
  }, 1500);
});

// Send a byte sequence to the agent if the terminal socket is live.
function sendToAgent(data) {
  if (activeSocket && activeSocket.readyState === WebSocket.OPEN) {
    activeSocket.send(data);
  }
}

// CSI-u modifier field: 1 + bitmask (shift=1, alt=2, ctrl=4, super=8).
function csiuModifiers(e) {
  return 1 + (e.shiftKey ? 1 : 0) + (e.altKey ? 2 : 0) + (e.ctrlKey ? 4 : 0) + (e.metaKey ? 8 : 0);
}

// The terminal dialog hosts a live TUI. Two classes of keys need help reaching
// the agent, both intercepted here (capture phase, before ghostty-web's own
// keydown handler) and stopped so the <dialog> never dismisses and ghostty
// never double-encodes:
//
//   - ESC: sent as a plain `\x1b`. ghostty would encode it as `\x1b[27u`
//     (Kitty protocol), which shows up as garbage in bubbletea TUIs.
//   - Ctrl+<digit/symbol> (e.g. C-1, C-2): these have no legacy byte, so
//     ghostty (not in Kitty mode here) drops the Ctrl and sends a bare `1`.
//     We emit the CSI-u sequence (`\x1b[<code>;<mods>u`) ourselves; tmux's
//     extended-keys then bridges it to the agent. Ctrl+<letter> is left
//     alone: it already maps to a C0 control byte that ghostty sends.
document.getElementById("terminal-dialog").addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation();
    sendToAgent("\x1b");
    return;
  }
  if (e.ctrlKey && e.key.length === 1 && !/[a-z]/i.test(e.key)) {
    e.preventDefault();
    e.stopPropagation();
    sendToAgent(`\x1b[${e.key.codePointAt(0)};${csiuModifiers(e)}u`);
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

// Chromium/WebKit sometimes leave a stale composited layer of a closed dialog
// (backdrop-filter invalidation bugs), ghosting it over the board until a full
// navigation. Toggling a transform on the board forces the compositor to
// rebuild its layers after any dialog closes.
function nudgeRepaint() {
  const board = document.getElementById("board");
  board.style.transform = "translateZ(0)";
  requestAnimationFrame(() => {
    board.style.transform = "";
  });
}

document.querySelectorAll("dialog").forEach((d) => {
  d.addEventListener("close", nudgeRepaint);
});

// --- Dialogs ---

// New task dialog
async function openNewTaskDialog() {
  const select = document.getElementById("task-project");
  select.innerHTML = projects.map((p) => `<option value="${p.id}">${esc(p.name)}</option>`).join("");
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
  const label = submitBtn.querySelector(".btn-label");
  const originalLabel = label.textContent;
  submitBtn.disabled = true;
  submitBtn.classList.add("btn-loading");
  label.textContent = "Creating…";

  try {
    await API.createCard({ prompt, projectId });
    dialog.close();
    document.getElementById("task-prompt").value = "";
  } catch (err) {
    alert(err.message);
    submitBtn.disabled = false;
    submitBtn.classList.remove("btn-loading");
    label.textContent = originalLabel;
  }
}

document.getElementById("new-task-dialog").addEventListener("close", () => {
  const submitBtn = document.getElementById("new-task-dialog").querySelector("button[type=submit]");
  submitBtn.disabled = false;
  submitBtn.classList.remove("btn-loading");
  submitBtn.querySelector(".btn-label").textContent = "Create Task";
});

// Projects dialog
document.getElementById("btn-projects").addEventListener("click", async () => {
  renderProjects();
  await populateAgentSelect();
  document.getElementById("projects-dialog").showModal();
});

// Populate the agent <select> with the YAML configs found under ~/.agents.
async function populateAgentSelect() {
  const select = document.getElementById("proj-agent");
  let agents = [];
  try {
    agents = await API.listAgents();
  } catch {
    // Leave the list empty on error; the default agent is used server-side.
  }
  const options = [];
  for (const a of agents) {
    options.push(`<option value="${esc(a)}">${esc(a.split("/").pop())}</option>`);
  }
  select.innerHTML = options.join("");
}

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
    <div class="project-item" draggable="true" data-id="${p.id}">
      <span class="project-drag" title="Drag to reorder">☰</span>
      <div class="project-info">
        <div class="project-name">${esc(p.name)}</div>
        <div class="project-paths">
          ${p.repoPath ? `<div class="project-path"><span class="project-path-label">repo</span>${esc(shortenPath(p.repoPath))}</div>` : ""}
          ${p.agent ? `<div class="project-path"><span class="project-path-label">agent</span>${esc(p.agent.split("/").pop())}</div>` : ""}
        </div>
      </div>
      <button class="btn btn-small btn-danger" onclick="deleteProject('${p.id}')" title="Delete project">✕</button>
    </div>
  `).join("");
  enableProjectDragReorder(list);
}

// Drag-and-drop reordering of project items. On drop, the new order is
// persisted and the board refreshed.
function enableProjectDragReorder(list) {
  let dragged = null;

  list.querySelectorAll(".project-item").forEach((item) => {
    item.addEventListener("dragstart", () => {
      dragged = item;
      item.classList.add("dragging");
    });

    item.addEventListener("dragend", async () => {
      item.classList.remove("dragging");
      dragged = null;
      const ids = [...list.querySelectorAll(".project-item")].map((el) => el.dataset.id);
      try {
        await API.reorderProjects(ids);
        await refresh();
      } catch (err) {
        alert(err.message);
      }
    });

    item.addEventListener("dragover", (e) => {
      e.preventDefault();
      if (!dragged || dragged === item) return;
      const rect = item.getBoundingClientRect();
      const after = e.clientY > rect.top + rect.height / 2;
      list.insertBefore(dragged, after ? item.nextSibling : item);
    });
  });
}

window.deleteProject = async (id) => {
  await API.deleteProject(id);
  await refresh();
  renderProjects();
};

// --- Folder picker ---

// Server-side folder browser bound to the repo-path input.
let folderCurrentPath = "";

async function loadFolder(path) {
  let data;
  try {
    data = await API.browse(path);
  } catch (err) {
    alert(err.message);
    return;
  }
  folderCurrentPath = data.path;
  document.getElementById("folder-current").textContent = data.path;

  const rows = [];
  if (data.parent) {
    rows.push(`<button type="button" class="folder-entry folder-up" data-path="${esc(data.parent)}">⤴ ..</button>`);
  }
  for (const dir of data.dirs) {
    const full = `${data.path.replace(/\/$/, "")}/${dir}`;
    rows.push(`<button type="button" class="folder-entry" data-path="${esc(full)}">📁 ${esc(dir)}</button>`);
  }
  document.getElementById("folder-list").innerHTML = rows.join("") || `<div class="empty-column">No subfolders</div>`;
}

document.getElementById("proj-repo-browse").addEventListener("click", () => {
  const current = document.getElementById("proj-repo").value.trim();
  loadFolder(current);
  document.getElementById("folder-dialog").showModal();
});

document.getElementById("folder-list").addEventListener("click", (e) => {
  const btn = e.target.closest(".folder-entry");
  if (btn) loadFolder(btn.dataset.path);
});

document.getElementById("folder-select").addEventListener("click", () => {
  document.getElementById("proj-repo").value = folderCurrentPath;
  document.getElementById("folder-dialog").close();
});

// Columns dialog: a full editor for the board's columns. Rows can be
// reordered by their drag handle, renamed, given a new emoji and prompt,
// deleted, or added; nothing is persisted until Save posts the whole list.
document.getElementById("btn-columns").addEventListener("click", () => {
  renderColumnsEditor();
  document.getElementById("columns-dialog").showModal();
});

document.getElementById("col-add").addEventListener("click", () => {
  const list = document.getElementById("columns-list");
  list.insertAdjacentHTML("beforeend", columnEditorItem({ id: "", name: "", emoji: "", prompt: "" }));
  const item = list.lastElementChild;
  wireColumnEditorItem(item);
  item.querySelector(".col-name").focus();
});

document.getElementById("columns-dialog").querySelector("form").addEventListener("submit", async (e) => {
  e.preventDefault();

  const updates = [...document.querySelectorAll("#columns-list .column-edit-item")].map((el) => ({
    id: el.dataset.id,
    name: el.querySelector(".col-name").value.trim(),
    emoji: el.querySelector(".col-emoji").value.trim(),
    prompt: el.querySelector(".col-prompt").value,
  }));

  try {
    columns = await API.updateColumns(updates);
    renderBoard();
    document.getElementById("columns-dialog").close();
  } catch (err) {
    alert(err.message);
  }
});

function columnEditorItem(col) {
  return `
    <div class="column-edit-item" data-id="${esc(col.id)}">
      <div class="column-edit-row">
        <span class="column-drag" title="Drag to reorder">☰</span>
        <input class="col-emoji" type="text" value="${esc(col.emoji)}" placeholder="🔨" title="Emoji" autocomplete="off">
        <input class="col-name" type="text" value="${esc(col.name)}" placeholder="Column name" required autocomplete="off" autocorrect="off" autocapitalize="off" spellcheck="false">
        <button type="button" class="btn btn-small btn-danger col-delete" title="Delete column">✕</button>
      </div>
      <textarea class="col-prompt" rows="3" placeholder="No prompt (manual column)" autocomplete="off" autocorrect="off" autocapitalize="off" spellcheck="false">${esc(col.prompt)}</textarea>
    </div>
  `;
}

function renderColumnsEditor() {
  const list = document.getElementById("columns-list");
  list.innerHTML = columns.map((col) => columnEditorItem(col)).join("");
  list.querySelectorAll(".column-edit-item").forEach(wireColumnEditorItem);
}

// Drag-to-reorder and delete for one column editor row. The row is only
// draggable while the handle is held, so text selection in the inputs keeps
// working.
let draggedColumnItem = null;

function wireColumnEditorItem(item) {
  const list = item.parentElement;

  item.querySelector(".column-drag").addEventListener("mousedown", () => {
    item.draggable = true;
    // A press without a drag must not leave the row draggable, or text
    // selection in its inputs would start a row drag. During a real drag the
    // mouseup fires at drop time, after dragstart, so the drag is unaffected.
    document.addEventListener("mouseup", () => {
      item.draggable = false;
    }, { once: true });
  });

  item.addEventListener("dragstart", () => {
    draggedColumnItem = item;
    item.classList.add("dragging");
  });

  item.addEventListener("dragend", () => {
    item.draggable = false;
    item.classList.remove("dragging");
    draggedColumnItem = null;
  });

  item.addEventListener("dragover", (e) => {
    e.preventDefault();
    if (!draggedColumnItem || draggedColumnItem === item) return;
    const rect = item.getBoundingClientRect();
    const after = e.clientY > rect.top + rect.height / 2;
    list.insertBefore(draggedColumnItem, after ? item.nextSibling : item);
  });

  // Deletion is local until Save; the server rejects deleting a column that
  // still has cards.
  item.querySelector(".col-delete").addEventListener("click", () => {
    item.remove();
  });
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
  document.getElementById("diff-project").textContent =
    cards.find((c) => c.id === cardId)?.project || "";
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

// esc escapes a string for safe interpolation in HTML, including
// attribute values (quotes must be escaped there).
const ESC_CHARS = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ESC_CHARS[c]);
}

// formatCost renders a card's cumulative agent cost as a dollar amount, or an
// empty string when there is nothing to show yet. Sub-cent costs keep more
// precision so an early, cheap turn is not rounded away to "$0.00".
function formatCost(cost) {
  const n = Number(cost);
  if (!n || n <= 0) return "";
  const decimals = n < 1 ? 3 : 2;
  return "$" + n.toFixed(decimals);
}

// prLabel renders a pull request URL as a short "PR #<n>" label by pulling the
// number out of a GitHub-style .../pull/<n> path. When the URL has no such
// number (an unexpected shape), it falls back to a plain "PR" so the link
// still shows something meaningful.
function prLabel(url) {
  const m = String(url ?? "").match(/\/pull\/(\d+)/);
  return m ? `PR #${m[1]}` : "PR";
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

// Cache the home directory so project paths can be shown with a "~" prefix.
API.browse().then((d) => { homeDir = d.path; }).catch(() => {});

refresh();
connectSSE();
