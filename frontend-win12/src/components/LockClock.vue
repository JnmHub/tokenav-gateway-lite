<script setup>
import { onUnmounted, ref } from 'vue'

/* 锁屏时间独立组件：只在等待页显示，登录表单不会被每秒重渲染 */
const now = ref(new Date())
const pad = (n) => String(n).padStart(2, '0')
const iv = setInterval(() => (now.value = new Date()), 1000)
onUnmounted(() => clearInterval(iv))
</script>

<template>
  <div class="w12-lock-time">{{ pad(now.getHours()) }}:{{ pad(now.getMinutes()) }}</div>
  <div class="w12-lock-date">{{ now.getMonth() + 1 }}月{{ now.getDate() }}日 星期{{ '日一二三四五六'[now.getDay()] }}</div>
  <div class="w12-lock-hint">点击任意位置解锁</div>
</template>
