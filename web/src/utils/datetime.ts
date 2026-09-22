/** 日期时间格式化器只构造一次: Intl.DateTimeFormat 实例化代价约 141µs,
 * 逐行构造会在长列表里累积(收件箱/别名列表都按行调用)。 */
const dateTimeFormatter = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
})

/** 把时间格式化为 "2026/08/04 10:05"(24 小时制)。 */
export function formatDateTime(date: Date): string {
  return dateTimeFormatter.format(date)
}

/** 把分钟数格式化为 "N 小时"(整点)或 "N 分钟"。任务表单与任务列表共用。 */
export function formatIntervalMinutes(minutes: number): string {
  return minutes % 60 === 0 ? `${minutes / 60} 小时` : `${minutes} 分钟`
}
