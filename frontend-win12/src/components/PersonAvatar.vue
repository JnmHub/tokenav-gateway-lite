<script setup>
/* 原版 win12 的紫蓝渐变人形头像（设置页/开始菜单共用） */
defineProps({
  size: { type: Number, default: 56 },
  radius: { type: String, default: '50%' },
})
</script>

<template>
  <span
    :style="{
      width: size + 'px',
      height: size + 'px',
      borderRadius: radius,
      background: '#88888838',
      display: 'inline-flex',
      alignItems: 'center',
      justifyContent: 'center',
      flex: 'none',
    }"
  >
    <svg :width="size * 0.62" :height="size * 0.62" viewBox="0 0 56 56">
      <defs>
        <linearGradient id="w12-pa-g" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stop-color="#b48cf2" />
          <stop offset="1" stop-color="#4d9de8" />
        </linearGradient>
      </defs>
      <circle cx="28" cy="19" r="10" fill="url(#w12-pa-g)" />
      <path d="M7 53 C7 39 16 34 28 34 C40 34 49 39 49 53 Z" fill="url(#w12-pa-g)" />
    </svg>
  </span>
</template>
