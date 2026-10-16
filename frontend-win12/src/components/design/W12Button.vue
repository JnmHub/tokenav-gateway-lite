<script setup>
import { Button } from 'ant-design-vue'
defineProps({ variant: { type: String, default: 'secondary' }, compact: Boolean })
</script>
<template>
  <Button class="w12-design" :type="variant === 'primary' ? 'primary' : variant === 'text' ? 'text' : 'default'" :danger="variant === 'danger'" :size="compact ? 'small' : 'middle'">
    <template v-if="$slots.icon" #icon><slot name="icon" /></template><slot />
  </Button>
</template>
