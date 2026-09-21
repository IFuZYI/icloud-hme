package server

// aliasLabelLibrary 是内置的常用服务名称库。自动创建任务按顺序循环取用，
// 仅作为本地管理标签，不代表别名已被该服务实际使用。
var aliasLabelLibrary = []string{
	"GitHub", "GitLab", "Google Workspace", "Microsoft", "Notion", "Slack",
	"Discord", "Linear", "Jira", "Figma", "Canva", "Vercel", "Netlify",
	"Cloudflare", "DigitalOcean", "AWS", "Azure", "OpenAI", "Anthropic",
	"Perplexity", "Proton", "Dropbox", "OneDrive", "Google Drive", "Zoom",
	"腾讯会议", "飞书", "微信", "支付宝", "淘宝", "京东", "美团", "饿了么",
	"哔哩哔哩", "抖音", "小红书", "微博", "知乎", "Steam", "Epic Games",
	"PlayStation", "Nintendo", "Spotify", "Apple Music", "Netflix", "YouTube",
	"Prime Video", "Airbnb", "Booking", "携程", "滴滴", "LinkedIn", "Medium",
	"Substack", "Reddit", "Telegram", "Signal", "WhatsApp", "X",
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
	for _, candidate := range aliasLabelLibrary {
		if label == candidate {
			return true
		}
	}
	return false
}
