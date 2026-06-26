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

	// 收集要保留的参数。注意：磁链的值（尤其是 xt=urn:btih:<hash>）必须原样
	// 透传，绝不能再做百分号编码——PikPak 离线下载只认字面量 "urn:btih:"，
	// 一旦冒号被编码成 %3A，磁链就无法被识别为种子，云端只会生成一个无效条目，
	// 多文件种子也就无法展开。这里直接保留原始 value，并手动拼接查询串。
	var xt []string
	var dn []string
	var trackers []string
	for key, values := range query {
		switch strings.ToLower(key) {
		case "xt":
			for _, value := range values {
				if value = strings.TrimSpace(value); value != "" {
					xt = append(xt, value)
				}
			}
		case "dn":
			for _, value := range values {
				if value = strings.TrimSpace(value); value != "" {
					dn = append(dn, value)
				}
			}
		case "tr":
			for _, value := range values {
				if value = strings.TrimSpace(value); value != "" {
					trackers = append(trackers, value)
				}
			}
		}
	}

	// 对 tracker 地址排序，确保同一磁链总是生成相同的清洗结果
	sort.Strings(trackers)

	var params []string
	for _, value := range xt {
		params = append(params, "xt="+value)
	}
	for _, value := range dn {
		// 显示名称可能含有空格等字符，按 query 规则编码，但 xt/tr 保持原样
		params = append(params, "dn="+url.QueryEscape(value))
	}
	for _, value := range trackers {
		params = append(params, "tr="+value)
	}

	if len(params) == 0 {
		return magnet
	}

	return "magnet:?" + strings.Join(params, "&")
}
