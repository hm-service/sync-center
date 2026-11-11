<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
import Login from "./components/Login.vue";
import Panel from "./components/Panel.vue";

const path = ref(location.pathname);
const isLogin = () => path.value === "/auth";

function follow() {
  path.value = location.pathname;
}

// 登录成功后原地切换视图，刷新时仍由服务端按 cookie 决定返回哪个页面。
function navigate(event: Event) {
  const target = (event as CustomEvent<string>).detail;
  history.pushState({}, "", target);
  path.value = target;
}

onMounted(() => {
  window.addEventListener("popstate", follow);
  window.addEventListener("app:navigate", navigate);
});

onUnmounted(() => {
  window.removeEventListener("popstate", follow);
  window.removeEventListener("app:navigate", navigate);
});
</script>

<template>
  <Login v-if="isLogin()" />
  <Panel v-else />
</template>
