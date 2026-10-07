package server

import (
	"math"
	"sort"
	"time"
	// 内嵌 IANA 时区数据库：调度时区跟随账号代理出口 IP，即使运行环境
	// (精简容器/scratch) 未安装 tzdata 也能正确加载 America/Denver 等时区。
	_ "time/tzdata"
)

// ====================================================================
// 拟人调度模型（分层非齐次泊松过程）
//
// 以「分层非齐次泊松过程」逼近真实用户的创建节奏：
//   - 日周期：每小时活跃度权重曲线（早高峰 / 午休低谷 / 晚高峰 / 凌晨低谷）；
//   - 周周期：周末作息整体后移（睡眠窗口与曲线顺延 1 小时）；
//   - 个体差异：用户画像（早鸟 / 夜猫子 / 标准 / 活跃），由任务种子稳定抽取；
//   - 成簇性：会话模型——Hawkes 自激概率决定下一间隔属于同一会话（短间隔）
//     还是新会话（长间隔），形成自然的「一阵忙、一阵歇」；
//   - 长尾：会话内 / 会话间间隔均取对数正态分布（非指数分布）；
//   - 配额自校正：目标间隔 = 剩余可用时间 ÷ 剩余配额，每步重算，
//     并预留「后续配额最短所需跨度」，使当日总数在睡前收敛；
//   - 硬约束：任何连续 60 分钟窗口内创建尝试 ≤ maxCreationsPerHour，
//     相邻两次自动尝试 ≥ minCreationSpacing（见 creationGuard 门控）。
//
// 时区：全部作息计算使用任务时区（跟随账号代理出口 IP 的地理时区，
// 见 account.Manager.TimezoneFor）；调用方传入的 now 会被换算到该时区。
//
// 计算复杂度 O(1)：每次调度仅常数次浮点运算与 ≤24 次的小循环。
// ====================================================================

const (
	// rollingHourWindow / maxCreationsPerHour 是账号级创建红线：
	// 任意连续 60 分钟窗口内，创建尝试（成功或失败、人工或自动）不超过 5 个。
	rollingHourWindow   = time.Hour
	maxCreationsPerHour = 5
	// minCreationSpacing 为相邻两次自动创建尝试的最短间隔（会话内突发地板）；
	// 60 分钟窗口上限由 maxCreationsPerHour 兜住。
	minCreationSpacing = 3 * time.Minute
	// sessionBaseContinue 是会话自激概率的下限（与上次创建时间无关的基础概率）。
	sessionBaseContinue = 0.10
	// wakeJitterMax 是睡醒后首次采样附加的随机延迟上限，避免每天同一分钟准时开工。
	wakeJitterMax = 20 * time.Minute
	// 会话内/会话间间隔的对数正态参数（中位数系数 × base，对数标准差）。
	// E[lognormal(m,σ)] = m·exp(σ²/2)；采样时按此期望反解 base（见 sampleAutoDelay）。
	continueMedianFactor   = 0.4
	continueSigma          = 0.5
	newSessionMedianFactor = 2.5
	newSessionSigma        = 0.55
)

// curve 是一天 24 小时的活跃度权重曲线（0 表示睡眠时段）。
type curve struct{ w [24]float64 }

// persona 描述一类真实用户的作息与节奏特征。
type persona struct {
	Name string
	// SleepStart/SleepEnd 为工作日的睡眠窗口 [start, end)（小时，可跨零点）；
	// 周末整体后移 1 小时（见 sleepingAt）。
	SleepStart int
	SleepEnd   int
	// HawkesAlpha/SessionTau 为会话自激参数：alpha 为自激幅度（0-1），
	// tau 为自激衰减时间常数（小时）。
	HawkesAlpha float64
	SessionTau  float64

	weekday curve
	weekend curve
	// avgWeekday/avgWeekend 为曲线在清醒小时上的平均权重（预计算，避免热路径重复求和）。
	avgWeekday float64
	avgWeekend float64
}

// newPersona 构建画像：weekend 曲线为 weekday 顺延 1 小时（睡眠窗口同步顺延）。
func newPersona(name string, sleepStart, sleepEnd int, alpha, tau float64, w [24]float64) *persona {
	p := &persona{
		Name:        name,
		SleepStart:  sleepStart,
		SleepEnd:    sleepEnd,
		HawkesAlpha: alpha,
		SessionTau:  tau,
		weekday:     curve{w: w},
	}
	p.weekend = curve{w: shiftWeights(w, 1)}
	p.avgWeekday = curveAvg(&p.weekday)
	p.avgWeekend = curveAvg(&p.weekend)
	return p
}

// shiftWeights 把权重曲线整体顺延 by 小时（循环）。
func shiftWeights(w [24]float64, by int) [24]float64 {
	var out [24]float64
	for h := 0; h < 24; h++ {
		out[(h+by)%24] = w[h]
	}
	return out
}

// curveAvg 返回曲线在清醒小时（权重 > 0）上的平均权重。
func curveAvg(c *curve) float64 {
	sum, n := 0.0, 0
	for _, w := range c.w {
		if w > 0 {
			sum += w
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return sum / float64(n)
}

// personaTable 为全部画像。首个元素（standard）是未知画像名的兜底。
var personaTable = []*persona{
	newPersona("standard", 23, 5, 0.55, 1.2, [24]float64{
		5: .35, 6: .60, 7: .90, 8: 1.1, 9: 1.2, 10: 1.1, 11: .95, 12: .70, 13: .60,
		14: .85, 15: .95, 16: .90, 17: .80, 18: .95, 19: 1.2, 20: 1.4, 21: 1.0, 22: .60,
	}),
	newPersona("early_bird", 22, 3, 0.50, 1.0, [24]float64{
		3: .40, 4: .60, 5: .90, 6: 1.1, 7: 1.3, 8: 1.2, 9: 1.0, 10: .95, 11: .85,
		12: .65, 13: .60, 14: .80, 15: .85, 16: .80, 17: .75, 18: .85, 19: 1.0, 20: 1.1, 21: .70,
	}),
	newPersona("night_owl", 3, 9, 0.60, 1.3, [24]float64{
		9: .45, 10: .60, 11: .75, 12: .80, 13: .70, 14: .75, 15: .85, 16: .95, 17: 1.0,
		18: 1.1, 19: 1.2, 20: 1.35, 21: 1.35, 22: 1.2, 23: 1.1, 0: .95, 1: .75, 2: .50,
	}),
	newPersona("active", 1, 6, 0.65, 1.1, [24]float64{
		6: .50, 7: .80, 8: 1.0, 9: 1.1, 10: 1.1, 11: 1.0, 12: .75, 13: .65, 14: .85,
		15: .95, 16: 1.0, 17: .95, 18: 1.0, 19: 1.25, 20: 1.45, 21: 1.4, 22: 1.2, 23: 1.0, 0: .75,
	}),
}

// personaForSeed 由任务种子稳定映射到一个画像（splitmix64 混合，避免相邻种子聚集）。
func personaForSeed(seed uint64) *persona {
	x := seed + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	return personaTable[x%uint64(len(personaTable))]
}

// personaByName 按名称查找画像；未知名称回退到 standard。
func personaByName(name string) *persona {
	for _, p := range personaTable {
		if p.Name == name {
			return p
		}
	}
	return personaTable[0]
}

// isWeekend 判断是否为周末（周六 / 周日）。
func isWeekend(t time.Time) bool {
	wd := t.Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

// hourInWindow 判断小时 h 是否落在睡眠窗口 [start, end)（可跨零点）。
func hourInWindow(h, start, end int) bool {
	h = ((h % 24) + 24) % 24
	if start == end {
		return false
	}
	if start < end {
		return h >= start && h < end
	}
	return h >= start || h < end
}

// sleepingHour 判断工作日的小时 h 是否处于睡眠时段。
func (p *persona) sleepingHour(h int) bool {
	return hourInWindow(h, p.SleepStart, p.SleepEnd)
}

// sleepingAt 判断时刻 t（画像本地时间）是否处于睡眠时段（周末顺延 1 小时）。
func (p *persona) sleepingAt(t time.Time) bool {
	if isWeekend(t) {
		return hourInWindow(t.Hour(), p.SleepStart+1, p.SleepEnd+1)
	}
	return p.sleepingHour(t.Hour())
}

// sleepStartAt 返回 t 之后（或当前）的下一个睡眠开始时刻。
//
// 用日历日算术（time.Date 的日溢出归一化）而非 Add(24h)：DST 切换日
// 并非 24 小时（柏林 2026-10-25 有 25 小时），Add(24h) 会停在当日 23:00，
// 使 sleepStartAt 返回过去时刻并让 NextRun 回退（虚拟时钟仿真曾抓到）。
func (p *persona) sleepStartAt(t time.Time) time.Time {
	start := p.SleepStart
	if isWeekend(t) {
		start++
	}
	hh := start % 24
	s := time.Date(t.Year(), t.Month(), t.Day(), hh, 0, 0, 0, t.Location())
	if !s.After(t) {
		s = time.Date(s.Year(), s.Month(), s.Day()+1, hh, 0, 0, 0, t.Location())
	}
	return s
}

// wakeAfter 返回从 t（睡眠中）到下一次睡醒的时长；清醒时返回 0。
func (p *persona) wakeAfter(t time.Time) time.Duration {
	if !p.sleepingAt(t) {
		return 0
	}
	end := p.SleepEnd
	if isWeekend(t) {
		end++
	}
	hh := end % 24
	wake := time.Date(t.Year(), t.Month(), t.Day(), hh, 0, 0, 0, t.Location())
	if !wake.After(t) {
		wake = time.Date(wake.Year(), wake.Month(), wake.Day()+1, hh, 0, 0, 0, t.Location())
	}
	// 跨周末边界：工作日入睡、周末顺延的醒点可能仍落在睡眠窗口内，继续顺延。
	if p.sleepingAt(wake) {
		wake = wake.Add(p.wakeAfter(wake))
	}
	return wake.Sub(t)
}

// nextWakeAt 返回 t 之后（含当前）的下一个睡醒时刻。
func (p *persona) nextWakeAt(t time.Time) time.Time {
	if p.sleepingAt(t) {
		return t.Add(p.wakeAfter(t))
	}
	ss := p.sleepStartAt(t)
	ref := ss.Add(time.Minute)
	return ref.Add(p.wakeAfter(ref))
}

// lastWakeAt 返回 t 之前（含当前）最近一次睡醒时刻（t 为画像本地时间）。
//
// 作息周期以「睡醒」为分界：一个周期 = [本次睡醒, 下次睡醒)，其中清醒段
// [睡醒, 入睡) 是连续窗口；跨零点的作息因此落在同一周期内，使每日配额与
// 活动窗口自然对齐（见 cycleDateAt）。
// 用日历日算术而非 Add(hours)：DST 切换日加小时会落到错误的挂钟时刻。
func (p *persona) lastWakeAt(t time.Time) time.Time {
	var best time.Time
	for _, d := range []int{0, -1} {
		day := time.Date(t.Year(), t.Month(), t.Day()+d, 0, 0, 0, 0, t.Location())
		h := p.SleepEnd
		if isWeekend(day) {
			h++
		}
		w := time.Date(day.Year(), day.Month(), day.Day(), h%24, 0, 0, 0, t.Location())
		if !w.After(t) && w.After(best) {
			best = w
		}
	}
	return best
}

// cycleDateAt 返回 t 所属作息周期的日期串（以「本次睡醒」的日期为准）。
//
// 每日配额按此重置：对 23-5 这类标准作息，重置点(05:00)与日历零点几乎无差；
// 对 03-9 这类跨零点作息，重置点(09:00)与清醒窗口起点对齐，避免配额在
// 半夜被截断导致「凌晨扎堆、白天沉默」或跨周期丢失。
func (p *persona) cycleDateAt(t time.Time) string {
	return taskDate(p.lastWakeAt(t))
}

// weightAt 返回时刻 t 的活跃度权重（周末用顺延后的曲线）。
func (p *persona) weightAt(t time.Time) float64 {
	if isWeekend(t) {
		return p.weekend.w[t.Hour()]
	}
	return p.weekday.w[t.Hour()]
}

// avgWeightAt 返回时刻 t 所属曲线（工作日 / 周末）的平均权重。
func (p *persona) avgWeightAt(t time.Time) float64 {
	if isWeekend(t) {
		return p.avgWeekend
	}
	return p.avgWeekday
}

// sessionContinueProbability 返回「下一次创建仍属于当前会话」的概率。
//
// Hawkes 自激：刚创建后概率最高（base + alpha），随时间指数衰减到 base。
func sessionContinueProbability(p *persona, sinceLast time.Duration) float64 {
	if sinceLast < 0 {
		sinceLast = 0
	}
	decay := math.Exp(-sinceLast.Hours() / p.SessionTau)
	return sessionBaseContinue + p.HawkesAlpha*decay
}

// sinceLastAttempt 返回距上一次创建尝试的时长；无记录时视为很久以前。
func sinceLastAttempt(g creationGuard, now time.Time) time.Duration {
	if g.LastAttempt == "" {
		return 24 * time.Hour
	}
	t, err := time.Parse(time.RFC3339Nano, g.LastAttempt)
	if err != nil {
		return 24 * time.Hour
	}
	d := now.Sub(t)
	if d < 0 {
		return 0
	}
	return d
}

// logNormalDuration 采样一个对数正态分布的时长（median 为中位数，单位纳秒）。
//
// 用 Box-Muller 变换从两个均匀随机数得到标准正态样本；结果钳到 ≥1ns。
func logNormalDuration(median, sigma float64, rnd func() float64) time.Duration {
	u1 := rnd()
	if u1 < 1e-12 {
		u1 = 1e-12
	}
	u2 := rnd()
	z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
	d := median * math.Exp(sigma*z)
	if d < 1 {
		d = 1
	}
	return time.Duration(d)
}

// fitSpanFor 返回从同一时刻起排下 attempts 次创建尝试所需的最短时间跨度。
//
// 受滚动小时上限约束（每 60 分钟最多 5 次）与最短间隔约束：
// 前 5 次可在 4 个最短间隔内完成，第 6 次须等 1 小时，以此类推。
// attempts ≤ 1 时无需额外跨度。
func fitSpanFor(attempts int) time.Duration {
	if attempts <= 1 {
		return 0
	}
	k := attempts - 1
	return rollingHourWindow*time.Duration(k/maxCreationsPerHour) + minCreationSpacing*time.Duration(k%maxCreationsPerHour)
}

// sampleAutoDelay 采样下一次自动创建前的等待时长。
//
// now 必须是画像所在时区的本地时间（调用方负责换算）；返回值为相对时长，
// 与时区无关。guard 提供账号级历史尝试（用于 Hawkes 自激与滚动小时门控）。
//
// 采样流程（分层 NHPP）：
//  1. 睡眠时段 → 顺延到睡醒（附加少量随机抖动）；
//  2. 可用时间 = 到入睡（本段清醒窗口终点）的时长；时间已排不下剩余配额
//     时顺延到睡醒，配额留待下一段窗口继续；
//  3. 目标间隔 g = (可用时间 − 配额所需跨度) ÷ 剩余配额（自校正）；
//  4. Hawkes 自激概率 pc 决定会话内/会话间混合；按 E[gap]=g 反解对数正态
//     的尺度 base（保证期望收敛，不因随机采样系统性偏离配额）；
//  5. 按当前小时活跃度权重缩放（高峰更密、低谷更疏）；
//  6. 钳制到 [最短间隔, 配额可行上限]，再按滚动小时门控修正落点。
func sampleAutoDelay(p *persona, now time.Time, done, quota int, guard creationGuard, rnd func() float64) time.Duration {
	// 睡眠中: 顺延到睡醒。
	if p.sleepingAt(now) {
		return p.wakeAfter(now) + time.Duration(rnd()*float64(wakeJitterMax))
	}
	remaining := quota - done
	if remaining < 1 {
		remaining = 1
	}
	end := p.sleepStartAt(now) // 本段清醒窗口的终点（入睡时刻）
	avail := end.Sub(now)
	// limitOffset 为本尝试最晚落点距入睡的时长：为「含本次在内的剩余配额」
	// 预留足以排下的时间（含滚动小时上限与最短间隔），保证配额在睡前收敛。
	limitOffset := fitSpanFor(remaining) + 2*minCreationSpacing + time.Minute
	// 本段清醒时间已排不下剩余配额 → 顺延到睡醒（配额留待下一段窗口继续）。
	if avail < limitOffset {
		return p.nextWakeAt(now).Sub(now) + time.Duration(rnd()*float64(wakeJitterMax))
	}
	g := (avail - limitOffset) / time.Duration(remaining)
	// Hawkes 自激：pc 决定会话内/会话间混合比例。
	pc := sessionContinueProbability(p, sinceLastAttempt(guard, now))
	// 反解尺度 base 使混合期望 E[gap] = g（E[lognormal(m,σ)] = m·exp(σ²/2)）。
	expect := pc*continueMedianFactor*math.Exp(continueSigma*continueSigma/2) +
		(1-pc)*newSessionMedianFactor*math.Exp(newSessionSigma*newSessionSigma/2)
	base := float64(g) / expect
	var gap time.Duration
	if rnd() < pc {
		gap = logNormalDuration(base*continueMedianFactor, continueSigma, rnd) // 会话内：短间隔
	} else {
		gap = logNormalDuration(base*newSessionMedianFactor, newSessionSigma, rnd) // 会话间：长间隔
	}
	// NHPP 变测度：活跃度高的时段间隔更短（低谷更疏）。
	if w := p.weightAt(now); w > 0 {
		gap = time.Duration(float64(gap) * p.avgWeightAt(now) / w)
	}
	// 上限: 单次间隔不超过公平份额的 3 倍——重尾采样偶尔会抽出远超平均的
	// 间隔, 若放任它吃掉整个窗口, 剩余配额会被迫在睡前以最短间隔扎堆
	// (跨零点作息下表现为单日超量)。3× 保留长尾特征, 同时保证配额均匀铺开。
	if fair := 3 * g; gap > fair {
		gap = fair
	}
	if bound := avail - limitOffset; gap > bound {
		gap = bound
	}
	if gap < minCreationSpacing {
		gap = minCreationSpacing
	}
	cand := now.Add(gap)
	// 滚动小时门控：落点使窗口超限时向后顺延到窗口空出。
	for i := 0; i < 8; i++ {
		w := hourlyWait(guard, cand)
		if w <= 0 {
			break
		}
		cand = cand.Add(w)
	}
	if !cand.Before(end) || p.sleepingAt(cand) {
		// 越界：改试最早可行槽位（打包），仍越界则顺延到睡醒。
		earliest := now.Add(minCreationSpacing)
		for i := 0; i < 8; i++ {
			w := hourlyWait(guard, earliest)
			if w <= 0 {
				break
			}
			earliest = earliest.Add(w)
		}
		if earliest.Before(end) && !p.sleepingAt(earliest) {
			return earliest.Sub(now)
		}
		return p.nextWakeAt(cand).Sub(now) + time.Duration(rnd()*float64(wakeJitterMax))
	}
	return cand.Sub(now)
}

// recentAttempts 解析 guard 中的历史尝试时刻并按时间升序返回（忽略无法解析的条目）。
func recentAttempts(g creationGuard) []time.Time {
	out := make([]time.Time, 0, len(g.RecentAttempts))
	for _, s := range g.RecentAttempts {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// hourlyWait 返回为使 at 时刻满足「任意连续 60 分钟内创建尝试 ≤ maxCreationsPerHour」
// 需要额外等待的时长；已满足时返回 0。
//
// 采用最严格的闭区间口径：计数窗口为 [at-60min, at]，放行时刻 = 需滑出的最早一次
// 尝试 + 60 分钟 + 1 秒余量。由此归纳可证「任意闭区间 [t, t+60min] 内的尝试数
// ≤ 上限」恒成立（对用户可读的任意「8:00-9:00」「8:30-9:30」窗口都成立）。
func hourlyWait(g creationGuard, at time.Time) time.Duration {
	times := recentAttempts(g)
	lo := at.Add(-rollingHourWindow)
	var win []time.Time
	for _, t := range times {
		if !t.Before(lo) && !t.After(at) {
			win = append(win, t)
		}
	}
	if len(win) < maxCreationsPerHour {
		return 0
	}
	// 需要让 len(win)-(max-1) 次最早的尝试滑出窗口（含 1 秒余量使开边界成立）。
	need := len(win) - maxCreationsPerHour + 1
	wait := win[need-1].Add(rollingHourWindow).Add(time.Second).Sub(at)
	if wait < 0 {
		return 0
	}
	return wait
}

// spacingWait 返回为使 at 时刻距上次尝试满 minCreationSpacing 需要等待的时长。
func spacingWait(g creationGuard, at time.Time) time.Duration {
	if g.LastAttempt == "" {
		return 0
	}
	last, err := time.Parse(time.RFC3339Nano, g.LastAttempt)
	if err != nil {
		return 0
	}
	if elapsed := at.Sub(last); elapsed < minCreationSpacing {
		return minCreationSpacing - elapsed
	}
	return 0
}

// advanceGuard 记录一次创建尝试：更新最近尝试列表（裁剪出窗口的旧条目）与最后尝试时间。
func advanceGuard(g creationGuard, now time.Time) creationGuard {
	stamp := now.Format(time.RFC3339Nano)
	cutoff := now.Add(-2 * rollingHourWindow)
	keep := make([]string, 0, len(g.RecentAttempts)+1)
	for _, s := range g.RecentAttempts {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil && t.After(cutoff) {
			keep = append(keep, s)
		}
	}
	keep = append(keep, stamp)
	return creationGuard{LastAttempt: stamp, RecentAttempts: keep}
}

// gateWaitAt 返回 guard 在 at 时刻需要等待的时长（最短间隔与滚动小时上限取大）。
func gateWaitAt(g creationGuard, at time.Time) time.Duration {
	wait := spacingWait(g, at)
	if w := hourlyWait(g, at); w > wait {
		wait = w
	}
	return wait
}

// creationGateWaitLocked 返回账号级创建门控需要等待的时长：取最短间隔与滚动
// 小时上限两者的较大值；已满足时返回 0。调用方必须持有 m.mu。
func (m *autoTaskManager) creationGateWaitLocked(accountID string, now time.Time) time.Duration {
	return gateWaitAt(m.creationGuards[accountID], now)
}
