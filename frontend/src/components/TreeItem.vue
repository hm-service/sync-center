<script setup lang="ts">
import { ref } from "vue";
import type { Entry } from "../api";
import { formatSize, formatStamp } from "../format";
import type { TreeNode } from "../tree";

const props = defineProps<{ node: TreeNode; depth: number; active: string }>();
const emit = defineEmits<{ pick: [file: Entry] }>();
const open = ref(props.depth < 1);
</script>

<template>
  <div
    v-if="node.file"
    class="file-row"
    :class="{ active: active === node.file.path }"
    @click="emit('pick', node.file)"
  >
    <span class="name">{{ node.name }}</span>
    <span class="meta">{{ formatSize(node.file.size) }} · {{ formatStamp(node.file.mtime) }}</span>
  </div>
  <template v-else>
    <div class="dir" :class="{ open }" @click="open = !open">{{ node.name }}</div>
    <div v-show="open">
      <TreeItem
        v-for="child in node.children"
        :key="child.path"
        :node="child"
        :depth="depth + 1"
        :active="active"
        @pick="emit('pick', $event)"
      />
    </div>
  </template>
</template>
