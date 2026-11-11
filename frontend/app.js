const state = { namespace: null, seq: null, files: [] };

const el = (id) => document.getElementById(id);

async function api(path) {
  const response = await fetch(path);
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}

function formatSize(bytes) {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  return (bytes / 1024 / 1024).toFixed(1) + " MB";
}

function formatTime(seconds) {
  return new Date(seconds * 1000).toLocaleString();
}

function emptyBox(box, message) {
  box.innerHTML = "";
  const note = document.createElement("p");
  note.className = "empty";
  note.textContent = message;
  box.appendChild(note);
}

async function loadNamespaces() {
  const rows = await api("/ui/api/namespaces");
  const box = el("namespaces");
  box.innerHTML = "";
  if (!rows.length) {
    emptyBox(box, "没有 namespace");
    return;
  }
  rows.forEach((row) => {
    const button = document.createElement("button");
    button.dataset.name = row.name;
    button.textContent = row.name;
    const small = document.createElement("small");
    small.textContent = "更新于 " + formatTime(row.updatedAt);
    button.appendChild(small);
    button.onclick = () => selectNamespace(row.name, button);
    box.appendChild(button);
  });
}

async function selectNamespace(name, button) {
  state.namespace = name;
  state.seq = null;
  state.files = [];
  document.querySelectorAll("#namespaces button").forEach((b) => b.classList.remove("active"));
  button.classList.add("active");
  emptyBox(el("files"), "先选一个 snapshot");
  emptyBox(el("preview"), "点一个文件查看");

  const data = await api("/ui/api/namespaces/" + encodeURIComponent(name) + "/snapshots?limit=20");
  const box = el("snapshots");
  box.innerHTML = "";
  if (!data.snapshots.length) {
    emptyBox(box, "没有快照");
    return;
  }
  data.snapshots.forEach((row) => {
    const item = document.createElement("button");
    item.dataset.seq = row.seq;
    item.textContent = "seq " + row.seq;
    const small = document.createElement("small");
    small.textContent = formatTime(row.createdAt) + " · " + formatSize(row.totalBytes);
    item.appendChild(small);
    item.onclick = () => selectSnapshot(row.seq, item);
    box.appendChild(item);
  });
}

async function selectSnapshot(seq, button) {
  state.seq = seq;
  document.querySelectorAll("#snapshots button").forEach((b) => b.classList.remove("active"));
  button.classList.add("active");
  emptyBox(el("preview"), "点一个文件查看");

  const data = await api("/ui/api/namespaces/" + encodeURIComponent(state.namespace) + "/snapshots/" + seq);
  state.files = data.manifest.files;
  renderFiles(data);
}

function buildTree(files) {
  const root = { children: new Map(), file: null };
  files.forEach((file) => {
    const parts = file.path.split("/");
    let node = root;
    for (let i = 0; i < parts.length - 1; i++) {
      if (!node.children.has(parts[i])) {
        node.children.set(parts[i], { children: new Map(), file: null });
      }
      node = node.children.get(parts[i]);
    }
    node.children.set(parts[parts.length - 1], { children: new Map(), file });
  });
  return root;
}

function renderTree(node, depth, container) {
  const entries = Array.from(node.children.entries()).sort((a, b) => {
    const aIsDir = a[1].file === null;
    const bIsDir = b[1].file === null;
    if (aIsDir !== bIsDir) return aIsDir ? -1 : 1;
    return a[0].localeCompare(b[0]);
  });

  entries.forEach(([name, child]) => {
    if (child.file) {
      const row = document.createElement("div");
      row.className = "file-row";
      row.dataset.path = child.file.path;
      row.style.paddingLeft = 12 + depth * 14 + "px";

      const label = document.createElement("span");
      label.textContent = name;
      const size = document.createElement("span");
      size.className = "size";
      size.textContent = formatSize(child.file.size);

      row.appendChild(label);
      row.appendChild(size);
      row.onclick = () => previewFile(child.file, row);
      container.appendChild(row);
      return;
    }

    const details = document.createElement("details");
    details.open = depth < 1;

    const summary = document.createElement("summary");
    summary.style.paddingLeft = depth * 14 + "px";
    summary.textContent = name + "/";

    const inner = document.createElement("div");
    renderTree(child, depth + 1, inner);

    details.appendChild(summary);
    details.appendChild(inner);
    container.appendChild(details);
  });
}

function renderFiles(data) {
  const box = el("files");
  box.innerHTML = "";
  const meta = document.createElement("div");
  meta.className = "meta";
  meta.textContent = "seq " + data.seq + " · " + formatTime(data.createdAt) + " · " + data.manifest.files.length + " 个文件";
  box.appendChild(meta);

  if (!data.manifest.files.length) {
    emptyBox(box, "空快照");
    return;
  }

  const tree = document.createElement("div");
  renderTree(buildTree(data.manifest.files), 0, tree);
  box.appendChild(tree);
}

const TEXT_EXT = ["yaml", "yml", "txt", "json", "md", "log", "lua", "toml", "ini", "conf"];

function isText(path) {
  const dot = path.lastIndexOf(".");
  if (dot < 0) return false;
  return TEXT_EXT.indexOf(path.slice(dot + 1).toLowerCase()) >= 0;
}

const PREVIEW_LIMIT = 256 * 1024;

async function previewFile(file, row) {
  document.querySelectorAll(".file-row").forEach((item) => item.classList.remove("active"));
  row.classList.add("active");

  const url = "/ui/api/blob/" + file.hash + "?name=" + encodeURIComponent(file.path);
  const box = el("preview");
  box.innerHTML = "";

  if (file.size > PREVIEW_LIMIT) {
    const note = document.createElement("p");
    note.className = "meta";
    note.textContent = formatSize(file.size) + " · 超过 256 KB，不预览";
    const link = document.createElement("a");
    link.href = url;
    link.textContent = "下载 " + file.path;
    box.appendChild(note);
    box.appendChild(link);
    return;
  }

  if (isText(file.path)) {
    const response = await fetch(url);
    const text = await response.text();
    const pre = document.createElement("pre");
    pre.textContent = text;
    box.appendChild(pre);
  } else {
    const link = document.createElement("a");
    link.href = url;
    link.textContent = "下载 " + file.path;
    const size = document.createElement("p");
    size.className = "meta";
    size.textContent = formatSize(file.size) + " · 二进制文件";
    box.appendChild(size);
    box.appendChild(link);
  }
}

async function boot() {
  await loadNamespaces();
  const params = new URLSearchParams(location.search);
  const namespace = params.get("ns");
  if (!namespace) return;

  const namespaceButton = document.querySelector('#namespaces button[data-name="' + CSS.escape(namespace) + '"]');
  if (!namespaceButton) return;
  await selectNamespace(namespace, namespaceButton);

  const seq = params.get("seq");
  if (!seq) return;
  const snapshotButton = document.querySelector('#snapshots button[data-seq="' + CSS.escape(seq) + '"]');
  if (!snapshotButton) return;
  await selectSnapshot(Number(seq), snapshotButton);

  const filePath = params.get("file");
  if (!filePath) return;
  const file = state.files.find((item) => item.path === filePath);
  if (!file) return;
  const row = Array.from(document.querySelectorAll(".file-row")).find(
    (item) => item.dataset.path === filePath
  );
  if (row) await previewFile(file, row);
}

boot().catch((error) => {
  emptyBox(el("namespaces"), error.message);
});
