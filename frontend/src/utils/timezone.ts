/**
 * 轻量网关后台统一使用中国上海时区解释和展示人类可读时间。
 *
 * 后端与数据库仍然传输、保存 UTC 绝对时间；这里仅负责把同一个时间点
 * 转成管理员看到的日期，以及把日期输入框中的上海墙上时间转回 UTC。
 */
export const DEFAULT_TIME_ZONE = 'Asia/Shanghai'
export const DEFAULT_TIME_ZONE_OFFSET = '+08:00'

type DateParts = {
  year: number
  month: number
  day: number
  hour: number
  minute: number
  second: number
}

const shanghaiPartsFormatter = new Intl.DateTimeFormat('en-US', {
  timeZone: DEFAULT_TIME_ZONE,
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hourCycle: 'h23'
})

const pad2 = (value: number): string => String(value).padStart(2, '0')

function readPart(parts: Intl.DateTimeFormatPart[], type: Intl.DateTimeFormatPartTypes): number {
  return Number(parts.find((part) => part.type === type)?.value ?? 0)
}

export function getDefaultTimeZoneDateParts(value: string | number | Date = new Date()): DateParts {
  const date = value instanceof Date ? value : new Date(value)
  if (!Number.isFinite(date.getTime())) {
    throw new RangeError('Invalid date')
  }

  const parts = shanghaiPartsFormatter.formatToParts(date)
  return {
    year: readPart(parts, 'year'),
    month: readPart(parts, 'month'),
    day: readPart(parts, 'day'),
    hour: readPart(parts, 'hour'),
    minute: readPart(parts, 'minute'),
    second: readPart(parts, 'second')
  }
}

/** 把绝对时间格式化成上海日历日期（YYYY-MM-DD）。 */
export function formatDefaultTimeZoneDateInput(value: string | number | Date = new Date()): string {
  const parts = getDefaultTimeZoneDateParts(value)
  return `${parts.year}-${pad2(parts.month)}-${pad2(parts.day)}`
}

/** 把绝对时间格式化成上海 datetime-local 控件值。 */
export function formatDefaultTimeZoneDateTimeInput(value: string | number | Date): string {
  const parts = getDefaultTimeZoneDateParts(value)
  return `${parts.year}-${pad2(parts.month)}-${pad2(parts.day)}T${pad2(parts.hour)}:${pad2(parts.minute)}`
}

/**
 * 把 datetime-local 中没有时区的信息明确按上海时间解析。
 * 中国上海现行时区固定为 UTC+08:00，没有夏令时歧义。
 */
export function parseDefaultTimeZoneDateTimeInput(value: string): Date | null {
  const normalized = value.trim()
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d{1,3})?)?$/.test(normalized)) {
    return null
  }
  const date = new Date(`${normalized}${DEFAULT_TIME_ZONE_OFFSET}`)
  return Number.isFinite(date.getTime()) ? date : null
}

/** 按上海日历日期增减整天，避免受浏览器所在时区影响。 */
export function addDaysToDateInput(value: string, days: number): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return ''
  const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3]) + days))
  return `${date.getUTCFullYear()}-${pad2(date.getUTCMonth() + 1)}-${pad2(date.getUTCDate())}`
}

/** 返回两个 YYYY-MM-DD 日历日期之间的整天数。 */
export function dateInputDayDifference(start: string, end: string): number {
  const toOrdinal = (value: string): number => {
    const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
    if (!match) return Number.NaN
    return Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
  }
  return Math.ceil((toOrdinal(end) - toOrdinal(start)) / 86_400_000)
}

/** 把上海日期边界转换成 API 使用的 UTC RFC3339 时间。 */
export function dateInputBoundaryToISOString(value: string, endOfDay = false): string | undefined {
  const suffix = endOfDay ? 'T23:59:59.999' : 'T00:00:00'
  const parsed = parseDefaultTimeZoneDateTimeInput(`${value}${suffix}`)
  return parsed?.toISOString()
}

/** 生成上海时间口径的文件名时间戳。 */
export function formatDefaultTimeZoneFileTimestamp(value: string | number | Date = new Date()): string {
  const parts = getDefaultTimeZoneDateParts(value)
  return `${parts.year}${pad2(parts.month)}${pad2(parts.day)}-${pad2(parts.hour)}${pad2(parts.minute)}${pad2(parts.second)}`
}
