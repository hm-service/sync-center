<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { api, fetchOrLogin, type Entry, type Namespace, type Snapshot, type SnapshotMeta } from "../api";
import { formatSize, formatTime, isText, previewLimit } from "../format";
import { t, useLanguage } from "../i18n";
import { buildTree, type TreeNode } from "../tree";
import LanguageSwitch from "./LanguageSwitch.vue";
import TreeItem from "./TreeItem.vue";

type Preview =
  | { kind: "text"; text: string }
  | { kind: "link"; url: string; path: string; note: string };

const { current } = useLanguage();

const namespaces = ref<Namespace[]>([]);
const snapshots = ref<SnapshotMeta[]>([]);
const files = ref<Entry[]>([]);
const activeNamespace = ref("");
const activeSeq = ref<number | null>(null);
const activeFile = ref("");
const preview = ref<Preview | null>(null);
const failed = ref(false);

const tree = computed<TreeNode[]>(() => buildTree(files.value));

function timeOf(seconds: number): string {
  return formatTime(seconds, current.value);
}

async function loadNamespaces() {
  try {
    namespaces.value = await api<Namespace[]>("/ui/api/namespaces");
  } catch {
    failed.value = true;
    return;
  }
  if (namespaces.value.length) await selectNamespace(namespaces.value[0].name);
}

async function selectNamespace(name: string) {
  activeNamespace.value = name;
  activeSeq.value = null;
  activeFile.value = "";
  files.value = [];
  preview.value = null;
  try {
    // 服务端只保留最近若干快照，这里取全部并在界面上区分新旧
    const data = await api<{ namespace: string; snapshots: SnapshotMeta[] }>(
      "/ui/api/namespaces/" + encodeURIComponent(name) + "/snapshots?limit=100",
    );
    snapshots.value = data.snapshots;
  } catch {
    failed.value = true;
    return;
  }
  if (snapshots.value.length) await selectSnapshot(snapshots.value[0].seq);
}

async function selectSnapshot(seq: number) {
  activeSeq.value = seq;
  activeFile.value = "";
  preview.value = null;
  try {
    const data = await api<Snapshot>(
      "/ui/api/namespaces/" + encodeURIComponent(activeNamespace.value) + "/snapshots/" + seq,
    );
    files.value = data.manifest.files;
  } catch {
    failed.value = true;
  }
}

async function previewFile(file: Entry) {
  activeFile.value = file.path;
  const url = "/ui/api/blob/" + file.hash + "?name=" + encodeURIComponent(file.path);

  if (file.size > previewLimit) {
    preview.value = {
      kind: "link",
      url,
      path: file.path,
      note: t("ui.preview-too-large", { size: formatSize(file.size) }),
    };
    return;
  }
  if (!isText(file.path)) {
    preview.value = {
      kind: "link",
      url,
      path: file.path,
      note: t("ui.binary-file", { size: formatSize(file.size) }),
    };
    return;
  }
  try {
    const response = await fetchOrLogin(url);
    preview.value = { kind: "text", text: await response.text() };
  } catch {
    failed.value = true;
  }
}

async function boot() {
  const params = new URLSearchParams(location.search);
  await loadNamespaces();
  const namespace = params.get("ns");
  if (namespace) await selectNamespace(namespace);
  const seq = params.get("seq");
  if (seq) await selectSnapshot(Number(seq));
  const file = params.get("file");
  if (file) {
    const entry = files.value.find((item) => item.path === file);
    if (entry) await previewFile(entry);
  }
}

onMounted(boot);
</script>

<template>
  <div class="app">
    <header class="bar">
      <h1>sync-center</h1>
      <span class="muted">{{ t("ui.readonly") }}</span>
      <div class="spacer"></div>
      <LanguageSwitch />
    </header>

    <div class="grid">
      <section class="col">
        <h2>{{ t("ui.namespaces") }}</h2>
        <button
          v-for="namespace in namespaces"
          :key="namespace.name"
          class="item"
          :class="{ active: namespace.name === activeNamespace }"
          @click="selectNamespace(namespace.name)"
        >
          {{ namespace.name }}
          <small>{{ t("ui.updated-at", { time: timeOf(namespace.updatedAt) }) }}</small>
        </button>
        <p v-if="!namespaces.length" class="empty">{{ t("ui.no-namespaces") }}</p>
      </section>

      <section class="col">
        <h2>{{ t("ui.snapshots") }} · {{ snapshots.length }}</h2>
        <button
          v-for="(snapshot, index) in snapshots"
          :key="snapshot.seq"
          class="item snapshot"
          :class="{ active: snapshot.seq === activeSeq, latest: index === 0, stale: index > 0 }"
          @click="selectSnapshot(snapshot.seq)"
        >
          seq {{ snapshot.seq }}
          <small>{{ formatSize(snapshot.totalBytes) }} · {{ timeOf(snapshot.createdAt) }}</small>
        </button>
        <p v-if="!snapshots.length" class="empty">{{ t("ui.no-snapshots") }}</p>
      </section>

      <section class="col">
        <h2>{{ t("ui.files") }} · {{ files.length }}</h2>
        <TreeItem
          v-for="node in tree"
          :key="node.path"
          :node="node"
          :depth="0"
          :active="activeFile"
          @pick="previewFile"
        />
        <p v-if="!files.length" class="empty">{{ t("ui.empty-snapshot") }}</p>
      </section>

      <section class="col">
        <h2>{{ t("ui.preview") }}</h2>
        <pre v-if="preview?.kind === 'text'">{{ preview.text }}</pre>
        <div v-else-if="preview?.kind === 'link'">
          <p class="meta">{{ preview.note }}</p>
          <a :href="preview.url">{{ t("ui.download", { path: preview.path }) }}</a>
        </div>
        <p v-else class="empty">{{ failed ? t("ui.load-failed") : t("ui.click-file") }}</p>
      </section>
    </div>
  </div>
</template>
