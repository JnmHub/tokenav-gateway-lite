<script setup>
defineProps({ tone: { type: String, default: '' } })
</script>
<template><span class="w12-status" :class="tone"><slot /></span></template>
