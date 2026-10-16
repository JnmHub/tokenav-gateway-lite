<script setup>
import { InboxOutlined, ExclamationCircleOutlined, LoadingOutlined } from '@ant-design/icons-vue'
import W12Button from './W12Button.vue'
defineProps({ error: String, loading: Boolean, text: { type: String, default: '暂无数据' } })
const emit = defineEmits(['retry'])
</script>
<template><div class="w12-design w12-empty" :role="error ? 'alert' : 'status'"><LoadingOutlined v-if="loading" spin /><ExclamationCircleOutlined v-else-if="error" /><InboxOutlined v-else /><span>{{ loading ? '正在读取…' : error || text }}</span><W12Button v-if="error" @click="emit('retry')">重试</W12Button></div></template>
