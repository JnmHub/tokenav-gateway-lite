<script setup>
import { Select } from 'ant-design-vue'
defineOptions({ inheritAttrs: false })
defineProps({ value: { type: [String, Number, Array, Object], default: undefined } })
const emit = defineEmits(['update:value'])
</script>
<template>
  <span class="w12-design" style="display: contents"><Select v-bind="$attrs" :value="value" @update:value="emit('update:value', $event)">
    <template v-for="(_, name) in $slots" #[name]="scope"><slot :name="name" v-bind="scope || {}" /></template>
  </Select></span>
</template>
