import dayjs from 'dayjs'
import 'dayjs/locale/zh-cn'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import Antd from 'ant-design-vue'
import 'ant-design-vue/dist/reset.css'
import './styles/global.css'
import './styles/fluent.css'
// 主题风格包：按 core/themes.js 注册表逐个引入（扁平化等），css 内用
// :root[data-style=…] 作用域，仅在对应风格激活时生效
import './styles/themes/flat.css'
// 五案主题（A–E）：各自深浅两套令牌 + 材质，见文件头注释的「区分轴」
import './styles/themes/fluent.css'
import './styles/themes/indigo.css'
import './styles/themes/verdant.css'
import './styles/themes/obsidian.css'
import './styles/themes/deepsea.css'
// 外壳与样板令牌最后加载，避免旧主题的窗口覆盖破坏材料遮挡。
import './styles/shell.css'
import './styles/design/index.css'
import App from './App.vue'

dayjs.locale('zh-cn')

// 壁纸改为纯 CSS（core/wallpaper.js），无需预热图片缓存
createApp(App).use(createPinia()).use(Antd).mount('#app')
