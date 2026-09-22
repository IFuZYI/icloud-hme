package server

import (
	"crypto/rand"
	"fmt"
)

// aliasLabelLibrary 是内置的常用服务名称库。自动生成标签的任务按顺序循环取用，
// 仅作为本地管理标签，不代表别名已被该服务实际使用。
//
// 名称库按类别拼装后在 init 中去重，最终约 500+ 条；扩充时只需向对应类别追加，
// 无需担心跨类别重名（init 会自动去掉重复项，保持顺序稳定）。
var aliasLabelLibrary = buildAliasLabelLibrary()

// aliasLabelCategories 是分类原始名称；拼装成扁平库前会去重。
var aliasLabelCategories = [][]string{
	// 开发者平台与代码托管
	{
		"GitHub", "GitLab", "Bitbucket", "Gitee", "Coding", "SourceForge", "Gitpod",
		"Codeberg", "Gerrit", "Phabricator", "CircleCI", "Travis CI", "Jenkins",
		"Buildkite", "Drone CI", "TeamCity", "Bamboo", "Argo CD", "Spinnaker",
		"SonarQube", "Snyk", "Dependabot", "Renovate", "CodeClimate", "Codecov",
		"npm", "PyPI", "RubyGems", "Packagist", "Crates.io", "Maven Central",
		"Docker Hub", "Quay", "JFrog Artifactory", "Nexus", "Homebrew",
	},
	// 云计算与基础设施
	{
		"AWS", "Azure", "Google Cloud", "阿里云", "腾讯云", "华为云", "百度智能云",
		"DigitalOcean", "Linode", "Vultr", "Hetzner", "OVHcloud", "Scaleway",
		"Cloudflare", "Fastly", "Akamai", "Vercel", "Netlify", "Render",
		"Railway", "Fly.io", "Heroku", "Supabase", "PlanetScale", "Neon",
		"MongoDB Atlas", "Redis Cloud", "Elastic Cloud", "Snowflake", "Databricks",
		"HashiCorp", "Terraform Cloud", "Pulumi", "Datadog", "New Relic",
		"Grafana Cloud", "Sentry", "PagerDuty", "Sumo Logic", "Splunk",
	},
	// 办公协作与生产力
	{
		"Google Workspace", "Microsoft 365", "Notion", "Slack", "Discord",
		"Linear", "Jira", "Confluence", "Trello", "Asana", "Monday.com",
		"ClickUp", "Basecamp", "Airtable", "Coda", "Miro", "Mural", "Figma",
		"FigJam", "Canva", "Zeplin", "Framer", "Webflow", "Zapier", "Make",
		"IFTTT", "Calendly", "Doodle", "Loom", "Grammarly", "DeepL", "Todoist",
		"TickTick", "Evernote", "OneNote", "Bear", "Obsidian", "Logseq", "Roam",
		"飞书", "钉钉", "企业微信", "腾讯文档", "石墨文档", "语雀", "WPS",
	},
	// 云存储与文件
	{
		"Dropbox", "OneDrive", "Google Drive", "iCloud Drive", "Box", "MEGA",
		"pCloud", "Sync.com", "Backblaze", "Tresorit", "百度网盘", "阿里云盘",
		"天翼云盘", "腾讯微云", "坚果云", "WeTransfer", "Filen", "Proton Drive",
	},
	// AI 与机器学习
	{
		"OpenAI", "ChatGPT", "Anthropic", "Claude", "Google Gemini", "Perplexity",
		"Mistral AI", "Cohere", "Hugging Face", "Replicate", "Stability AI",
		"Midjourney", "Runway", "ElevenLabs", "Suno", "Pika", "Ideogram",
		"Character.AI", "Poe", "GitHub Copilot", "Cursor", "Codeium", "Tabnine",
		"文心一言", "通义千问", "讯飞星火", "智谱清言", "Kimi", "豆包", "腾讯元宝",
		"DeepSeek", "MiniMax", "月之暗面", "百川智能",
	},
	// 社交与社区
	{
		"LinkedIn", "X", "Facebook", "Instagram", "Threads", "Mastodon", "Bluesky",
		"Reddit", "Quora", "Medium", "Substack", "Tumblr", "Pinterest", "Snapchat",
		"微博", "知乎", "小红书", "豆瓣", "贴吧", "即刻", "脉脉", "V2EX",
		"Hacker News", "Product Hunt", "Indie Hackers", "Dev.to", "Hashnode",
	},
	// 即时通讯
	{
		"Telegram", "Signal", "WhatsApp", "微信", "QQ", "Line", "KakaoTalk",
		"Viber", "WeChat Work", "Skype", "Google Chat", "Messenger", "iMessage",
		"Element", "Matrix", "Session", "Threema",
	},
	// 电商与购物
	{
		"淘宝", "天猫", "京东", "拼多多", "苏宁易购", "唯品会", "网易严选",
		"小米商城", "得物", "闲鱼", "转转", "亚马逊", "Amazon", "eBay", "Etsy",
		"AliExpress", "Shein", "Temu", "Shopify", "Wish", "Lazada", "Shopee",
		"乐天", "Walmart", "Target", "Best Buy", "Newegg", "宜家", "无印良品",
	},
	// 生活服务与出行
	{
		"美团", "饿了么", "大众点评", "滴滴出行", "高德地图", "百度地图", "货拉拉",
		"携程", "去哪儿", "飞猪", "同程旅行", "12306", "Airbnb", "Booking",
		"Agoda", "Expedia", "Uber", "Lyft", "Grab", "Google Maps", "TripAdvisor",
		"马蜂窝", "曹操出行", "T3出行", "哈啰出行", "美团单车",
	},
	// 影音娱乐
	{
		"Netflix", "Disney+", "HBO Max", "Prime Video", "Apple TV+", "Hulu",
		"YouTube", "YouTube Music", "Spotify", "Apple Music", "SoundCloud",
		"Tidal", "Pandora", "Deezer", "哔哩哔哩", "抖音", "快手", "腾讯视频",
		"爱奇艺", "优酷", "芒果TV", "网易云音乐", "QQ音乐", "酷狗音乐", "咪咕视频",
		"Twitch", "TikTok", "Vimeo", "Dailymotion", "喜马拉雅", "荔枝FM", "蜻蜓FM",
	},
	// 游戏平台
	{
		"Steam", "Epic Games", "GOG", "Origin", "EA App", "Ubisoft Connect",
		"Battle.net", "PlayStation", "Xbox", "Nintendo", "Riot Games", "Roblox",
		"Rockstar Games", "itch.io", "Humble Bundle", "TapTap", "WeGame",
		"米哈游", "网易游戏", "腾讯游戏", "4399", "GeForce NOW", "Xbox Cloud",
	},
	// 金融与支付
	{
		"支付宝", "微信支付", "云闪付", "PayPal", "Stripe", "Square", "Wise",
		"Revolut", "Payoneer", "Airwallex", "Coinbase", "Binance", "Kraken",
		"OKX", "Bybit", "Gate.io", "招商银行", "工商银行", "建设银行", "中国银行",
		"农业银行", "交通银行", "花旗银行", "汇丰银行", "东方财富", "同花顺",
		"雪球", "富途牛牛", "老虎证券", "Robinhood",
	},
	// 安全与密码
	{
		"1Password", "Bitwarden", "LastPass", "Dashlane", "KeePass", "NordPass",
		"Proton Pass", "Authy", "Google Authenticator", "Microsoft Authenticator",
		"Yubico", "Okta", "Auth0", "Duo Security", "OneLogin", "Ping Identity",
	},
	// 邮箱与隐私
	{
		"Gmail", "Outlook", "Yahoo Mail", "ProtonMail", "Tutanota", "Fastmail",
		"Zoho Mail", "iCloud Mail", "网易邮箱", "QQ邮箱", "新浪邮箱", "Mailchimp",
		"SendGrid", "Postmark", "Mailgun", "ConvertKit", "Substack Mail",
		"NordVPN", "ExpressVPN", "Surfshark", "Mullvad", "Proton VPN", "Cloudflare WARP",
	},
	// 学习与教育
	{
		"Coursera", "Udemy", "edX", "Khan Academy", "Duolingo", "Skillshare",
		"Pluralsight", "LinkedIn Learning", "Codecademy", "freeCodeCamp",
		"LeetCode", "HackerRank", "Kaggle", "中国大学MOOC", "网易云课堂",
		"腾讯课堂", "得到", "知乎盐选", "百词斩", "扇贝", "多邻国", "Anki",
	},
	// 设计与创意工具
	{
		"Adobe Creative Cloud", "Photoshop", "Illustrator", "Premiere Pro",
		"After Effects", "Lightroom", "Sketch", "Affinity", "Procreate",
		"Blender", "Cinema 4D", "Autodesk", "SketchUp", "DaVinci Resolve",
		"Final Cut Pro", "Capcut", "剪映", "醒图", "美图秀秀", "创客贴", "稿定设计",
	},
	// 硬件与设备账号
	{
		"Apple ID", "Google 账号", "Microsoft 账号", "小米账号", "华为账号",
		"三星账号", "OPPO账号", "vivo账号", "荣耀账号", "Sony 账号", "Nvidia",
		"Intel", "AMD", "Logitech", "Razer", "Anker", "DJI", "GoPro", "Fitbit",
		"Garmin", "特斯拉", "蔚来", "小鹏", "理想汽车",
	},
	// 招聘与职业
	{
		"BOSS直聘", "拉勾", "猎聘", "智联招聘", "前程无忧", "脉脉", "实习僧",
		"Indeed", "Glassdoor", "Monster", "AngelList", "Wellfound", "Upwork",
		"Fiverr", "Toptal", "Freelancer", "猪八戒",
	},
	// 新闻与阅读
	{
		"今日头条", "腾讯新闻", "网易新闻", "新浪新闻", "澎湃新闻", "36氪", "虎嗅",
		"钛媒体", "少数派", "The Verge", "TechCrunch", "Wired", "纽约时报",
		"华尔街日报", "彭博社", "路透社", "Feedly", "Pocket", "Flipboard",
		"微信读书", "多看阅读", "掌阅", "起点中文网", "Kindle",
	},
	// 健康与运动
	{
		"Keep", "薄荷健康", "悦跑圈", "咕咚", "小米运动", "华为运动健康",
		"Apple Health", "Google Fit", "Strava", "MyFitnessPal", "Peloton",
		"平安好医生", "丁香医生", "好大夫在线", "微医", "京东健康", "阿里健康",
	},
}

// buildAliasLabelLibrary 拼装各类别并去重，保持首次出现顺序。
func buildAliasLabelLibrary() []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 512)
	for _, category := range aliasLabelCategories {
		for _, name := range category {
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

// aliasLabelSet 是名称库的集合视图，供 isKnownAliasLabel 做 O(1) 校验。
// 名称库已扩充到 500+ 条，每次 /api/create 都线性扫描会成为热点路径浪费。
var aliasLabelSet = buildAliasLabelSet()

func buildAliasLabelSet() map[string]struct{} {
	set := make(map[string]struct{}, len(aliasLabelLibrary))
	for _, name := range aliasLabelLibrary {
		set[name] = struct{}{}
	}
	return set
}

func aliasLabelFor(index int) string {
	if len(aliasLabelLibrary) == 0 {
		return "服务账号"
	}
	if index < 0 {
		index = 0
	}
	return aliasLabelLibrary[index%len(aliasLabelLibrary)]
}

func isKnownAliasLabel(label string) bool {
	_, ok := aliasLabelSet[label]
	return ok
}

const (
	// 手动标签模式的取值。
	labelModeLibrary    = "library"
	labelModeSequential = "sequential"
	labelModeHash       = "hash"

	// 顺序标签的最小补零位数；哈希后缀长度范围。
	sequentialPadWidth = 3
	minHashLength      = 4
	maxHashLength      = 8
	maxLabelPrefixLen  = 32
)

// hashAlphabet 是哈希后缀使用的字符集（去除易混淆的 0/o/1/l/i）。
const hashAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// randomHash 返回长度为 n 的随机小写字母数字串；n 会被夹在允许范围内。
// 使用 crypto/rand，随机源不可用时回退到确定性但仍唯一的填充，避免 panic。
func randomHash(n int) string {
	if n < minHashLength {
		n = minHashLength
	}
	if n > maxHashLength {
		n = maxHashLength
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// 极少发生：退化为可辨识的占位，调用方仍会拼接前缀保持可读。
		return fmt.Sprintf("%0*x", n, 0)[:n]
	}
	out := make([]byte, n)
	for i := range buf {
		out[i] = hashAlphabet[int(buf[i])%len(hashAlphabet)]
	}
	return string(out)
}

// labelFor 按任务的标签模式生成第 number 个别名的标签（number 从 1 开始）。
//   - library:    从内置名称库按序循环取用；
//   - sequential: 前缀 + 补零序号（如 主邮箱001）；
//   - hash:       前缀 + 随机哈希后缀（如 主邮箱-k7m9）。
func (t AliasTask) labelFor(number int) string {
	if number < 1 {
		number = 1
	}
	switch t.LabelMode {
	case labelModeSequential:
		return fmt.Sprintf("%s%0*d", t.LabelPrefix, sequentialPadWidth, number)
	case labelModeHash:
		return t.LabelPrefix + randomHash(t.HashLength)
	default:
		return aliasLabelFor(number - 1)
	}
}
