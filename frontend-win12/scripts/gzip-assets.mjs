// 构建后处理：为 dist/assets 下的文本资产生成 .gz 预压缩文件（Go embed 服务优先发送，
// 省约 3 倍传输；浏览器不支持 gzip 时回退原文件）。>1KB 才压缩（小文件收益低）。
import { gzipSync } from 'node:zlib'
import { readdirSync, statSync, readFileSync, writeFileSync } from 'node:fs'
import { join, extname, resolve } from 'node:path'

// 从 frontend-win12 运行（build 脚本的工作目录），dist 在仓库的 internal/web/dist
const DIST = resolve(process.cwd(), '../internal/web/dist')
const COMPRESSIBLE = new Set(['.js', '.css', '.svg', '.json', '.html', '.txt'])
const MIN_SIZE = 1024

let count = 0
let saved = 0
function walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    const st = statSync(p)
    if (st.isDirectory()) { walk(p); continue }
    if (name.endsWith('.gz')) continue
    const ext = extname(name).toLowerCase()
    if (!COMPRESSIBLE.has(ext) || st.size < MIN_SIZE) continue
    const raw = readFileSync(p)
    const gz = gzipSync(raw, { level: 9 })
    if (gz.length < raw.length * 0.9) {
      writeFileSync(p + '.gz', gz)
      count++
      saved += raw.length - gz.length
    }
  }
}
walk(DIST)
console.log(`[gzip-assets] 预压缩 ${count} 个文件，节省 ${(saved / 1024 / 1024).toFixed(2)}MB`)
