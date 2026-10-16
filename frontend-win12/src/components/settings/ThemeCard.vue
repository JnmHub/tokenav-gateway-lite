<script setup>
/* 主题外观设置卡（纯本地偏好，不涉及后端接口）
 * 组件化原则：
 *   - 风格清单来自 core/themes.js 注册表——新增风格无需改本组件；
 *   - 主题状态全部走 theme store（core/stores.js）。
 * 交互原则（用户要求）：五案主题做成「一排按钮、点一下整套切换」——
 * 配色 / 圆角 / 材质 / 桌面壁纸 / 图标强调色一起换，不用逐个调。
 * 五案自带强调色，故隐藏「主题色」预设；经典两套仍由预设驱动。 */
import { computed } from 'vue'
import { useThemeStore, THEME_PRESETS } from '../../core/stores'
import { CASE_STYLES, LEGACY_STYLES, styleById, styleOwnsAccent } from '../../core/themes'

const theme = useThemeStore()
const activeStyle = computed(() => styleById(theme.style))
const ownsAccent = computed(() => styleOwnsAccent(activeStyle.value))
</script>

<template>
  <div class="card theme-card">
    <div class="card-title">主题外观</div>
    <div class="card-sub">点一下整套切换：配色、圆角、材质、桌面壁纸一起换，本地保存、即时生效。</div>

    <!-- 五案主题：一排快速切换按钮（每案自带配色与专属壁纸） -->
    <div class="case-row" role="group" aria-label="主题风格">
      <button
        v-for="s in CASE_STYLES" :key="s.id" type="button" class="case-btn"
        :class="{ active: theme.style === s.id }"
        :title="s.desc"
        @click="theme.setStyle(s.id)"
      >
        <span class="case-chip" :style="{ background: `linear-gradient(135deg, ${s.accent.c1}, ${s.accent.c2})` }" />
        <span class="case-tag">{{ s.tag }}</span>
        <span class="case-name">{{ s.name }}</span>
      </button>
    </div>
    <div class="case-hint">{{ activeStyle.desc }}</div>

    <!-- 经典风格：沿用原有卡片式单选 -->
    <div class="style-grid">
      <button
        v-for="s in LEGACY_STYLES" :key="s.id" type="button" class="style-opt"
        :class="{ active: theme.style === s.id }" @click="theme.setStyle(s.id)"
      >
        <span class="s-check">✓</span>
        <span class="s-name">{{ s.name }}</span>
        <span class="s-desc">{{ s.desc }}</span>
      </button>
    </div>

    <div class="row"><span>深色模式</span><a-switch :checked="theme.dark" @change="(v) => theme.setDark(v)" /></div>
    <div class="row"><span>窗口 Mica 渐变</span><a-switch :checked="theme.mica" @change="(v) => theme.setMica(v)" /></div>

    <!-- 主题色：五案自带配色时不做成可选项（避免把主题调歪），只做说明 -->
    <div v-if="ownsAccent" class="row">
      <span>主题色</span>
      <span class="accent-note">
        <i class="accent-dot" :style="{ background: activeStyle.accent.primary }" />
        <em>随主题自带</em>
      </span>
    </div>
    <div v-else class="row">
      <span>主题色</span>
      <span class="presets">
        <button
          v-for="p in THEME_PRESETS" :key="p.name" type="button" class="preset-dot"
          :class="{ active: theme.preset.name === p.name }"
          :style="{ background: `linear-gradient(135deg, ${p.c1}, ${p.c2})` }"
          :title="p.name" @click="theme.setPreset(p)"
        />
      </span>
    </div>
  </div>
</template>

<style scoped>
.card { padding: 12px 16px; border-radius: 8px; background: var(--card); border: 1px solid var(--hr); }
.card-title { font-weight: 600; font-size: 13px; margin-bottom: 6px; }
.card-sub { font-size: 12px; color: var(--text2); margin: 0 0 10px; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 38px; }

/* 五案快速切换按钮：一排胶囊，超出换行（窄屏自然堆叠） */
.case-row { display: flex; flex-wrap: wrap; gap: 8px; }
.case-btn {
  display: inline-flex; align-items: center; gap: 8px; cursor: pointer;
  padding: 7px 12px; border-radius: 8px;
  border: 1px solid var(--hr); background: transparent; color: var(--text);
  transition: border-color 0.15s ease, background 0.15s ease;
}
.case-btn:hover { border-color: var(--accent); background: var(--hover); }
.case-btn.active {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}
.case-chip { width: 14px; height: 14px; border-radius: 4px; flex: none; }
.case-tag { font-size: 11px; font-weight: 600; color: var(--text2); }
.case-btn.active .case-tag { color: var(--accent); }
.case-name { font-size: 13px; font-weight: 500; }
.case-hint { margin: 8px 0 12px; font-size: 11px; line-height: 1.5; color: var(--text2); }

/* 经典风格单选卡：2 列网格，窄屏单列 */
.style-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; margin-bottom: 8px; }
@media (max-width: 640px) { .style-grid { grid-template-columns: 1fr; } }
.style-opt {
  position: relative; text-align: left; cursor: pointer;
  border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px;
  background: transparent; transition: border-color 0.15s ease, background 0.15s ease;
}
.style-opt:hover { border-color: var(--accent); }
.style-opt.active { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 10%, transparent); }
.s-check {
  position: absolute; top: 8px; right: 8px; width: 16px; height: 16px;
  border-radius: 50%; font-size: 10px; line-height: 16px; text-align: center;
  background: var(--accent); color: #fff; opacity: 0; transition: opacity 0.15s ease;
}
.style-opt.active .s-check { opacity: 1; }
.s-name { display: block; font-size: 13px; font-weight: 500; }
.s-desc { display: block; font-size: 11px; color: var(--text2); margin-top: 3px; line-height: 1.5; }

/* 主题色 */
.presets { display: flex; gap: 8px; }
.preset-dot {
  width: 22px; height: 22px; border-radius: 50%; cursor: pointer;
  border: 2px solid transparent; transition: border-color 0.15s ease, transform 0.15s ease;
}
.preset-dot:hover { transform: scale(1.1); }
.preset-dot.active { border-color: var(--text); }
.accent-note { display: inline-flex; align-items: center; gap: 8px; font-size: 12px; color: var(--text2); }
.accent-note em { font-style: normal; }
.accent-dot { width: 16px; height: 16px; border-radius: 50%; flex: none; }
</style>
