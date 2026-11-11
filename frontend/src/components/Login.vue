<script setup lang="ts">
import { ref } from "vue";
import { t } from "../i18n";
import LanguageSwitch from "./LanguageSwitch.vue";

const token = ref("");
const note = ref("");
const busy = ref(false);

async function submit() {
  if (busy.value) return;
  busy.value = true;
  note.value = "";
  try {
    const response = await fetch("/api/auth", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: token.value }),
    });
    if (response.status === 401) {
      note.value = t("ui.login-invalid");
      return;
    }
    if (!response.ok) {
      note.value = t("ui.login-failed", { status: response.status });
      return;
    }
    window.dispatchEvent(new CustomEvent("app:navigate", { detail: "/ui" }));
  } catch {
    note.value = t("ui.login-offline");
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="login-page">
    <form class="login-card" @submit.prevent="submit">
      <div class="bar" style="padding: 0; border: 0; margin-bottom: 6px">
        <h1>sync-center</h1>
        <div class="spacer"></div>
        <LanguageSwitch />
      </div>
      <p class="meta">{{ t("ui.login-title") }}</p>
      <input v-model="token" type="password" autocomplete="current-password" required autofocus />
      <button class="submit" type="submit" :disabled="busy">{{ t("ui.login-submit") }}</button>
      <p class="error">{{ note }}</p>
    </form>
  </div>
</template>
