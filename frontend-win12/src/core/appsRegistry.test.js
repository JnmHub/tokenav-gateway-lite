import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, existsSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import { parseAppLink } from './appLinks.js'

const source = relative => readFileSync(new URL(relative, import.meta.url), 'utf8')
const expectedIds = ['dashboard', 'settings', 'accounts', 'proxies', 'groups', 'channels', 'usage',
  'modelPrices', 'localKeys', 'riskControl', 'channelMonitor', 'auditLogs', 'opsCenter',
  'runtimeSummary', 'imageTasks', 'systemSettings', 'gatewayBinding', 'controlPlane', 'plugins']

function registry(href = 'https://fixture.invalid/') {
  const text = source('./apps.js'), opened = [], warnings = []
  // 执行真实注册表与打开逻辑；懒加载只登记函数，不加载框架、组件或网络。
  const body = text.replace(/^import .+$/gm, '').replace(/^export (const|function) /gm, '$1 ')
  const apps = runInNewContext(body + '\n({ APPS, getApp, openApp, openLocationApp })', {
    markRaw: value => value, lazy: loader => ({ loader }), parseAppLink,
    message: { warning: text => warnings.push(text) }, window: { location: { href } },
    useWindowsStore: () => ({ open(app, target) { opened.push({ app, target }); return app.id } }),
  })
  return { ...apps, opened, warnings }
}
const ids = apps => Array.from(apps, app => app.id)

test('轻量网关注册表保留19个后台应用，向导组件保留但不再懒加载或注册', () => {
  const apps = registry(), text = source('./apps.js')
  assert.deepEqual(ids(apps.APPS), expectedIds)
  assert.equal(new Set(ids(apps.APPS)).size, expectedIds.length)
  assert.equal(apps.getApp('setupWizard'), undefined)
  assert.ok(apps.APPS.every(app => app.name !== '初始化向导'))
  assert.doesNotMatch(text, /SetupWizard|setupWizard/)
  assert.ok(existsSync(new URL('../apps/SetupWizard.vue', import.meta.url)))
})

test('手机应用和开始菜单全部、搜索、最近入口共用无向导注册集合（源码/纯逻辑）', () => {
  const apps = registry(), desktop = source('../components/Desktop.vue'), menu = source('../components/StartMenu.vue')
  // 模板只核实集合来源；本测试不把源码与纯筛选断言当作真实GUI验收。
  assert.match(desktop, /v-for="app in APPS"[^>]*@click="openApp\(app\.id\)"/)
  assert.match(menu, /v-for="app in listed"[^>]*@click="launch\(app\.id\)"/)
  const core = menu.match(/const coreIds = (\[[^\r\n]+\])/)
  const listed = menu.match(/const listed = computed\(\(\) => ([\s\S]*?)\)\r?\nconst recent/)
  const recent = menu.match(/const recent = computed\(\(\) => ([^\r\n]+)\)/)
  assert.ok(core && listed && recent, '菜单集合定义必须可核对，不能默认断言通过')
  const list = (all, keyword) => runInNewContext('(' + listed[1] + ')', {
    APPS: apps.APPS, coreIds: runInNewContext(core[1]), showAll: { value: all }, keyword: { value: keyword },
  })
  assert.deepEqual(ids(list(true, '')), expectedIds)
  assert.deepEqual(ids(list(false, '')), ['dashboard', 'settings', 'accounts', 'groups', 'usage', 'opsCenter'])
  assert.deepEqual(ids(list(false, '初始化')), [])
  assert.deepEqual(ids(list(false, '分组')), ['groups'])
  const recentApps = runInNewContext('(' + recent[1] + ')', {
    store: { recentIds: ['setupWizard', 'groups', 'settings'] }, getApp: apps.getApp,
  })
  assert.deepEqual(ids(recentApps), ['groups', 'settings'])
})

test('openApp与地址白名单拒绝向导，其他19个应用仍可直接和经地址打开', () => {
  const apps = registry(), target = { tab: 'general', id: 7 }
  for (const id of expectedIds) {
    assert.equal(apps.openApp(id, target), id)
    assert.equal(apps.opened.at(-1).app, apps.getApp(id))
    assert.equal(apps.opened.at(-1).target, target)
    for (const href of [`https://fixture.invalid/?app=${id}`, `https://fixture.invalid/#app=${id}`]) {
      const linked = registry(href)
      linked.openLocationApp()
      assert.deepEqual(linked.opened.map(entry => entry.app.id), [id])
      assert.deepEqual(linked.warnings, [])
    }
  }
  const before = apps.opened.length
  assert.equal(apps.openApp('setupWizard'), null)
  assert.equal(apps.opened.length, before)
  assert.equal(apps.warnings.length, 1)
  for (const href of ['https://fixture.invalid/?app=setupWizard', 'https://fixture.invalid/#app=setupWizard']) {
    const linked = registry(href)
    assert.ok(parseAppLink(href, ids(linked.APPS)).error)
    linked.openLocationApp()
    assert.deepEqual(linked.opened, [])
    assert.equal(linked.warnings.length, 1)
  }
})
