package app

import (
	"net/url"
	"sort"
	"strings"
)

// normalizeMagnetLink 清洗磁链，去除多余参数，统一格式
// 参考 TelDriveManager 的实现，保留核心参数：xt, dn, tr
func normalizeMagnetLink(magnet string) string {
	magnet = strings.TrimSpace(magnet)
	if !strings.HasPrefix(strings.ToLower(magnet), "magnet:?") {
		return magnet
	}

	parsed, err := url.Parse(magnet)
	if err != nil {
		return magnet
	}

	query := parsed.Query()

	// 保留的核心参数
	keepParams := map[string]bool{
		"xt": true, // eXact Topic - 资源哈希
		"dn": true, // Display Name - 显示名称
		"tr": true, // TRacker - tracker 地址
	}

	cleaned := url.Values{}
	for key, values := range query {
		lowerKey := strings.ToLower(key)
		if keepParams[lowerKey] {
			for _, value := range values {
				if value = strings.TrimSpace(value); value != "" {
					cleaned.Add(key, value)
				}
			}
		}
	}

	// 对 tracker 地址排序，确保同一磁链总是生成相同的清洗结果
	if trackers := cleaned["tr"]; len(trackers) > 1 {
		sort.Strings(trackers)
		cleaned.Del("tr")
		for _, tr := range trackers {
			cleaned.Add("tr", tr)
		}
	}

	if len(cleaned) == 0 {
		return magnet
	}

	return "magnet:?" + cleaned.Encode()
}
