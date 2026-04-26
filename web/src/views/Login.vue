<script setup lang="ts">
import LanguageSelector from "@/components/LanguageSelector.vue";
import ThemeToggle from "@/components/ThemeToggle.vue";
import { useAuthService } from "@/services/auth";
import { LockClosedSharp } from "@vicons/ionicons5";
import { NIcon, NInput, useMessage } from "naive-ui";
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";

const authKey = ref("");
const loading = ref(false);
const router = useRouter();
const message = useMessage();
const { login } = useAuthService();
const { t } = useI18n();

const handleLogin = async () => {
  if (!authKey.value) {
    message.error(t("login.authKeyRequired"));
    return;
  }
  loading.value = true;
  const success = await login(authKey.value);
  loading.value = false;
  if (success) {
    router.push("/");
  }
};
</script>

<template>
  <div class="v3-root v3-login-root">
    <div class="v3-login-tools">
      <language-selector />
      <theme-toggle />
    </div>

    <div class="v3-login-stage">
      <div class="v3-login-brand">
        <div class="v3-chrome__logo" style="width: 40px; height: 40px; font-size: 18px">
          A
        </div>
        <div>
          <div
            style="
              font: 600 18px/1.2 var(--v3-sans);
              letter-spacing: -0.01em;
              color: var(--v3-ink);
            "
          >
            AutoGateway
          </div>
          <div style="font: 500 11px/1 var(--v3-mono); color: var(--v3-ink-3); margin-top: 4px">
            {{ t("v3.missionConsole") }}
          </div>
        </div>
      </div>

      <div class="v3-card v3-login-card">
        <div class="v3-card__head">
          <div>
            <div class="v3-card__title">
              {{ t("login.welcome") || "Sign in" }}
            </div>
            <div class="v3-card__sub">
              {{ t("login.welcomeDesc") || "Enter the AUTH_KEY configured on the server." }}
            </div>
          </div>
        </div>
        <div class="v3-card__body">
          <div
            class="v3-intake__paste-lbl"
            style="margin-bottom: 6px; display: flex; align-items: center; gap: 6px"
          >
            <n-icon :component="LockClosedSharp" :size="11" />
            AUTH_KEY
          </div>
          <n-input
            v-model:value="authKey"
            type="password"
            size="medium"
            :placeholder="t('login.authKeyPlaceholder') || 'sk-…'"
            @keyup.enter="handleLogin"
          />
          <button
            class="v3-btn v3-btn--accent v3-btn--lg"
            style="width: 100%; margin-top: 14px"
            :disabled="loading"
            @click="handleLogin"
          >
            {{ loading ? "Signing in…" : t("login.loginButton") || "Sign in" }}
          </button>
        </div>
      </div>

      <div class="v3-login-foot">
        <span style="font: 400 11px var(--v3-mono); color: var(--v3-ink-3)">
          {{ t("login.subtitle") || "AI Gateway · self-hosted" }}
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.v3-login-root {
  min-height: 100vh;
  display: grid;
  place-items: center;
  position: relative;
  padding: 24px;
}

.v3-login-tools {
  position: absolute;
  top: 18px;
  right: 18px;
  display: flex;
  align-items: center;
  gap: 6px;
}

.v3-login-stage {
  width: 100%;
  max-width: 380px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.v3-login-brand {
  display: flex;
  align-items: center;
  gap: 12px;
  justify-content: center;
}

.v3-login-card {
  box-shadow: var(--v3-shadow-md);
}

.v3-login-foot {
  text-align: center;
  margin-top: 4px;
}
</style>
