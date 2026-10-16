<script setup>
import { Table } from 'ant-design-vue'
defineOptions({ inheritAttrs: false })
</script>
<template>
  <div class="w12-design"><Table class="w12-table" size="small" v-bind="$attrs">
    <template v-for="(_, name) in $slots" #[name]="scope"><slot :name="name" v-bind="scope || {}" /></template>
  </Table></div>
</template>
