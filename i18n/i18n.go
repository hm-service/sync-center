package i18n

import (
	"fmt"
	"strings"
)

type message struct {
	zh string
	en string
}

// 每条文案同时带中英两列；key 缺失时原样返回，便于立刻发现漏配。
var messages = map[string]message{
	"error.unauthorized":        {"缺少或无效的令牌", "missing or invalid token"},
	"error.auth-required":       {"需要登录", "authentication required"},
	"error.invalid-token":       {"口令不正确", "invalid token"},
	"error.forbidden":           {"需要写权限", "write permission required"},
	"error.method-not-allowed":  {"方法不允许", "method not allowed"},
	"error.not-found":           {"资源不存在", "not found"},
	"error.internal":            {"服务内部错误", "internal error"},
	"error.readyz-token":        {"readyz 令牌不正确", "invalid readyz token"},
	"error.unavailable":         {"服务不可用", "service unavailable"},
	"error.invalid-digest":      {"摘要必须是 64 位小写十六进制", "digest must be 64 lowercase hex characters"},
	"error.digest-mismatch":     {"内容与摘要不一致", "content does not match digest"},
	"error.too-large":           {"文件超过大小上限", "blob exceeds size limit"},
	"error.blob-not-found":      {"文件不存在", "blob does not exist"},
	"error.leased":              {"namespace 已被占用", "namespace already has an active lease"},
	"error.lease-not-found":     {"租约不存在或已过期", "lease is absent or expired"},
	"error.lease-required":      {"提交需要有效租约", "an active lease is required"},
	"error.lease-id-required":   {"缺少 leaseId", "leaseId is required"},
	"error.invalid-body":        {"请求体不是合法 JSON", "request body is not valid JSON"},
	"error.invalid-manifest":    {"manifest 不合法：%s", "invalid manifest: %s"},
	"error.missing-blob":        {"缺少文件：%s", "missing blob: %s"},
	"error.blob-size-mismatch":  {"文件大小不一致：%s", "blob size mismatch: %s"},
	"error.conflict":            {"parent 与当前 head 不一致", "parent does not match head"},
	"error.no-snapshot":         {"还没有快照", "no snapshot yet"},
	"error.snapshot-not-found":  {"快照不存在", "snapshot does not exist"},
	"error.namespace-not-found": {"namespace 不存在", "namespace not found"},
	"error.invalid-limit":       {"limit 必须在 1 到 100 之间", "limit must be between 1 and 100"},
	"error.busy":                {"服务繁忙，请稍后重试", "service is busy, try again later"},
	"error.gc-running":          {"清理正在进行", "garbage collection is already running"},
}

// T 取本地化文案；未知 key 原样返回，缺失配置会直接出现在界面上。
func T(lang, key string, args ...any) string {
	entry, ok := messages[key]
	if !ok {
		return key
	}
	text := entry.en
	if lang == "zh" {
		text = entry.zh
	}
	if len(args) > 0 {
		text = fmt.Sprintf(text, args...)
	}
	return text
}

// Parse 从 Accept-Language 里挑出支持的语言，认不出时回退到英文。
func Parse(header string) string {
	for part := range strings.SplitSeq(header, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case strings.HasPrefix(tag, "zh"):
			return "zh"
		case strings.HasPrefix(tag, "en"):
			return "en"
		}
	}
	return "en"
}
