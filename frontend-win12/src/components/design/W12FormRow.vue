<script setup>
defineProps({ label: String, hint: String, forId: String })
</script>
<template><div class="w12-design w12-form-row"><label class="w12-form-label" :for="forId">{{ label }}</label><div class="w12-form-control"><slot /><span v-if="hint" class="w12-form-hint">{{ hint }}</span></div></div></template>
