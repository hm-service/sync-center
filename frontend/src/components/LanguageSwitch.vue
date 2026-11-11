<script setup lang="ts">
import { languages, t, useLanguage } from "../i18n";

const { current, setLanguage } = useLanguage();
</script>

<template>
  <div class="langs">
    <button
      v-for="lang in languages"
      :key="lang"
      type="button"
      :class="{ on: current === lang }"
      :title="t('ui.language')"
      @click="setLanguage(lang)"
    >
      {{ lang.toUpperCase() }}
    </button>
  </div>
</template>
