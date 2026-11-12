<script setup lang="ts">
import { computed, ref } from "vue";
import type { Entry } from "../api";
import { formatSize, formatStamp } from "../format";
import type { TreeNode } from "../tree";

const props = defineProps<{ node: TreeNode; depth: number; active: string }>();
const emit = defineEmits<{ pick: [file: Entry] }>();

// 目录默认展开第一层，更深的折叠起来，126 个文件不至于一上来就铺满
const open = ref(props.depth < 1);

// 每层缩进 14px；目录行左留 6px，文件行再让出三角位置，对齐到目录名下方
const dirStyle = computed(() => ({ paddingLeft: `${6 + props.depth * 14}px` }));
const fileStyle = computed(() => ({ paddingLeft: `${22 + props.depth * 14}px` }));
</script>

<template>
  <div
    v-if="node.file"
    class="file-row"
    :class="{ active: active === node.file.path }"
    :style="fileStyle"
    @click="emit('pick', node.file)"
  >
    <span class="name">{{ node.name }}</span>
    <span class="meta">{{ formatSize(node.file.size) }} · {{ formatStamp(node.file.mtime) }}</span>
  </div>
  <template v-else>
    <div class="dir" :class="{ open }" :style="dirStyle" @click="open = !open">{{ node.name }}</div>
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
