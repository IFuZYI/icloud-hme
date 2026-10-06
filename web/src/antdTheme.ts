import { theme as antdTheme } from 'antd'

/** antd 主题 token, 对齐项目设计系统 (web/src/styles.css 的 :root token)。
 *
 *  为什么需要: antd 默认样式(50px 高、6px 圆角、灰边框)会与相邻的
 *  SelectMenu/按钮明显错位; 且默认主题不感知暗色模式, 暗色系统下
 *  DatePicker 会是黑页面上的一块白斑(实测 rgb(255,255,255))。
 *
 *  暗色模式由 prefers-color-scheme 媒体查询驱动(与 styles.css 的
 *  暗色 token 块同一机制), 调用方负责在媒体查询变化时重新构建。
 */
const FONT_FAMILY =
  '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif'

/** 根据当前是否为暗色模式构建 antd 主题(可直接传给 ConfigProvider)。 */
export function buildAntdTheme(dark: boolean) {
  return {
    // 暗色模式使用 antd 暗色算法处理派生色(悬浮态/禁用态/浮层等)
    ...(dark ? { algorithm: antdTheme.darkAlgorithm } : {}),
    token: {
      controlHeight: 40,
      borderRadius: 11,
      // 以下 token 取自 styles.css 的 :root / 暗色媒体查询块
      colorBorder: dark ? '#48484a' : '#c7c7cc', // --color-border-strong
      colorText: dark ? '#f5f5f7' : '#1d1d1f', // --color-text
      colorBgContainer: dark ? '#1d1d1f' : '#ffffff', // --color-bg-raised
      // 「此刻」等链接按钮: 暗色算法默认 link 色在深底上仅 3.18:1,
      // 提亮到 #64b5ff (在 #1f1f1f 上 7.5:1) 满足 WCAG AA。
      colorLink: dark ? '#64b5ff' : '#0071e3',
      fontFamily: FONT_FAMILY,
    },
  }
}
