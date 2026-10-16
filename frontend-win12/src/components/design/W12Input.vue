<script setup>
import { Input } from 'ant-design-vue'
defineOptions({ inheritAttrs: false })
defineProps({ value: { type: [String, Number], default: '' } })
const emit = defineEmits(['update:value'])
</script>
<template>
  <span class="w12-design" style="display: contents"><Input v-bind="$attrs" :value="value" @update:value="emit('update:value', $event)">
    <template v-for="(_, name) in $slots" #[name]="scope"><slot :name="name" v-bind="scope || {}" /></template>
  </Input></span>
</template>
