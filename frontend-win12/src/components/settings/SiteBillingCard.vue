<script setup>
/* ============================================================
 * 站点类型卡（官方 v0.2.5 siteBillingMode 三态单选的前端派生实现）
 * - 由两个后端开关派生：subscription_enabled（订阅面开关，缺省 true）
 *   + payment_balance_disabled（余额充值关闭开关，缺省 false）
 * - 三态：充值&订阅 / 仅充值（保存时强制重新打开余额充值） / 仅订阅
 * - payment_balance_disabled 已在 SystemSettings.buildSaveBody 白名单豁免
 *   payment_* 剔除，随总表 PUT 提交
 * ============================================================ */
import { computed } from 'vue'
import { ShopOutlined, CrownOutlined, WalletOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  s: { type: Object, required: true },
  save: { type: Function, required: true },
})

/* 派生当前模式：subscription_enabled 响应缺省视为 true（opt-out 开关） */
const mode = computed(() => {
  const subOn = props.s.subscription_enabled !== false
  if (!subOn) return 'recharge_only'
  return props.s.payment_balance_disabled === true ? 'subscription_only' : 'recharge_and_subscription'
})

/* 单选切换：把三态映射回两个开关（仅充值时强制 payment_balance_disabled=false） */
const onModeChange = (e) => {
  const v = e.target.value
  if (v === 'recharge_and_subscription') {
    props.s.subscription_enabled = true
    props.s.payment_balance_disabled = false
  } else if (v === 'recharge_only') {
    props.s.subscription_enabled = false
    props.s.payment_balance_disabled = false
  } else {
    props.s.subscription_enabled = true
    props.s.payment_balance_disabled = true
  }
}

const options = [
  { value: 'recharge_and_subscription', label: '充值 & 订阅' },
  { value: 'recharge_only', label: '仅充值' },
  { value: 'subscription_only', label: '仅订阅' },
]
const hints = {
  recharge_and_subscription: '用户端同时展示余额充值与订阅购买入口',
  recharge_only: '隐藏订阅面，仅保留余额充值；保存时余额充值保持开启',
  subscription_only: '关闭余额充值下单，仅保留订阅购买',
}

const doSave = () => props.save('billing', {
  subscription_enabled: props.s.subscription_enabled !== false,
  payment_balance_disabled: props.s.payment_balance_disabled === true,
})
</script>

<template>
  <div class="card">
    <div class="card-title">站点类型</div>
    <div class="form">
      <div class="frow">
        <label>站点类型</label>
        <div class="fmain">
          <a-radio-group :value="mode" @change="onModeChange">
            <a-radio v-for="o in options" :key="o.value" :value="o.value">
              <ShopOutlined v-if="o.value === 'recharge_and_subscription'" />
              <WalletOutlined v-else-if="o.value === 'recharge_only'" />
              <CrownOutlined v-else />
              {{ o.label }}
            </a-radio>
          </a-radio-group>
        </div>
      </div>
      <div class="frow">
        <label>当前含义</label>
        <div class="fmain"><span class="static">{{ hints[mode] }}</span></div>
      </div>
    </div>
    <div class="savebar">
      <a-button type="primary" class="save" @click="doSave">保存站点类型</a-button>
    </div>
  </div>
</template>

<style scoped>
@import './settings.css';
</style>
