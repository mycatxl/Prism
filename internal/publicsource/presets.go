package publicsource

import (
	"fmt"
	"sort"
	"strings"
)

// Preset groups of well-known public proxy sources.
//
// A preset is only allowed to list a URL that has been verified to still yield
// nodes through the real subscription parser: TestPresetSourcesYieldNodes
// (gated on PUBLIC_SOURCE_LIVE_PRESET_TEST=1) downloads every URL below and
// fails the build if any of them stops producing nodes. That is the whole point
// of shipping presets instead of a wiki page of links: the list is executable
// and self-checking, so it cannot silently rot.
const (
	// PresetClassic is the single long-standing default source.
	PresetClassic = "classic"
	// PresetHTTP is HTTP/HTTPS proxy lists (ip:port based).
	PresetHTTP = "http"
	// PresetSOCKS is SOCKS4/SOCKS5 proxy lists.
	PresetSOCKS = "socks"
	// PresetNodes is node subscription lists (vmess/vless/trojan/ss/... URIs).
	PresetNodes = "nodes"
	// PresetAll expands to every static group.
	PresetAll = "all"
	// PresetNone selects no static source at all (gist-only setups).
	PresetNone = "none"
)

type presetGroup struct {
	name        string
	description string
	urls        []string
}

// presetGroups is ordered; PresetAll is the union of every group except
// classic (which it contains anyway) and none.
var presetGroups = []presetGroup{
	{
		name:        PresetClassic,
		description: "Resin 的长期默认来源（TheSpeedX HTTP 列表）",
		urls: []string{
			"https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt",
		},
	},
	{
		name:        PresetHTTP,
		description: "HTTP/HTTPS 代理列表",
		urls: []string{
			"https://raw.githubusercontent.com/zevtyardt/proxy-list/main/http.txt",
			"https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/all/data.txt",
			"https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt",
			"https://raw.githubusercontent.com/jetkai/proxy-list/main/online-proxies/txt/proxies-http.txt",
			"https://raw.githubusercontent.com/rdavydov/proxy-list/main/proxies/http.txt",
			"https://raw.githubusercontent.com/vakhov/fresh-proxy-list/master/http.txt",
			"https://raw.githubusercontent.com/ALIILAPRO/Proxy/main/http.txt",
			"https://raw.githubusercontent.com/mmpx12/proxy-list/master/http.txt",
			"https://raw.githubusercontent.com/clarketm/proxy-list/master/proxy-list-raw.txt",
			"https://raw.githubusercontent.com/saisuiu/Lionkings-Http-Proxys-Proxies/main/cnfree.txt",
			"https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt",
			"https://raw.githubusercontent.com/prxchk/proxy-list/main/http.txt",
			"https://raw.githubusercontent.com/roosterkid/openproxylist/main/HTTPS_RAW.txt",
			"https://raw.githubusercontent.com/ShiftyTR/Proxy-List/master/http.txt",
			// Sources contributed by resin-free-proxy-sync (mycatxl).
			"https://raw.githubusercontent.com/databay-labs/free-proxy-list/master/http.txt",
			"https://raw.githubusercontent.com/iplocate/free-proxy-list/main/protocols/http.txt",
			"https://raw.githubusercontent.com/sunny9577/proxy-scraper/master/generated/http_proxies.txt",
			"https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/http_all.txt",
			"https://api.proxyscrape.com/v4/free-proxy-list/get?request=displayproxies&protocol=http&timeout=10000&country=all&ssl=all&anonymity=all",
			"https://raw.githubusercontent.com/Zaeem20/FREE_PROXIES_LIST/master/http.txt",
			// Banner-prefixed plain text and an HTML page: handled by the format
			// normaliser in formats.go.
			"https://spys.me/proxy.txt",
			"https://www.my-proxy.com/free-proxy-list.html",
		},
	},
	{
		name:        PresetSOCKS,
		description: "SOCKS4/SOCKS5 代理列表",
		urls: []string{
			"https://raw.githubusercontent.com/hookzof/socks5_list/master/proxy.txt",
			"https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks5.txt",
			"https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks4.txt",
			"https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks5.txt",
			"https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks4.txt",
			"https://raw.githubusercontent.com/prxchk/proxy-list/main/socks5.txt",
			"https://raw.githubusercontent.com/roosterkid/openproxylist/main/SOCKS5_RAW.txt",
			// Sources contributed by resin-free-proxy-sync (mycatxl).
			"https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/socks5_all.txt",
			"https://api.proxyscrape.com/v4/free-proxy-list/get?request=displayproxies&protocol=socks5&timeout=10000&country=all",
			"https://raw.githubusercontent.com/iplocate/free-proxy-list/main/protocols/socks5.txt",
			"https://raw.githubusercontent.com/databay-labs/free-proxy-list/master/socks5.txt",
			"https://raw.githubusercontent.com/Zaeem20/FREE_PROXIES_LIST/master/socks5.txt",
		},
	},
	{
		name:        PresetNodes,
		description: "节点订阅（vmess/vless/trojan/ss 等 URI 或 base64 订阅）",
		urls: []string{
			"https://raw.githubusercontent.com/mheidari98/.proxy/main/all",
			"https://raw.githubusercontent.com/Epodonios/v2ray-configs/main/All_Configs_base64_Sub.txt",
			"https://raw.githubusercontent.com/mahdibland/V2RayAggregator/master/sub/sub_merge.txt",
			"https://raw.githubusercontent.com/ALIILAPRO/v2rayNG-Config/main/server.txt",
			"https://raw.githubusercontent.com/ermaozi/get_subscribe/main/subscribe/v2ray.txt",
			"https://raw.githubusercontent.com/peasoft/NoMoreWalls/master/list.txt",
			"https://raw.githubusercontent.com/ts-sf/fly/main/v2",
			"https://raw.githubusercontent.com/mahdibland/ShadowsocksAggregator/master/Eternity.txt",
			"https://raw.githubusercontent.com/Kwinshadow/TelegramV2rayCollector/main/sublinks/mix.txt",
			"https://raw.githubusercontent.com/ermaozi01/free_clash_vpn/main/subscribe/v2ray.txt",
			"https://raw.githubusercontent.com/Pawdroid/Free-servers/main/sub",
			"https://raw.githubusercontent.com/freefq/free/master/v2",
			"https://raw.githubusercontent.com/ripaojiedian/freenode/main/sub",
		},
	},
}

// DefaultPresetNames is the preset selection used when PUBLIC_SOURCE_PRESETS is
// unset. It preserves the historical single-source behaviour.
func DefaultPresetNames() []string {
	return []string{PresetClassic}
}

// PresetNames lists every selectable preset, including the two composite ones.
func PresetNames() []string {
	names := []string{PresetAll, PresetNone}
	for _, group := range presetGroups {
		names = append(names, group.name)
	}
	sort.Strings(names)
	return names
}

// PresetDescriptions returns "name: description" lines for help output.
func PresetDescriptions() []string {
	lines := []string{
		fmt.Sprintf("%s: 全部静态来源（http + socks + nodes）", PresetAll),
		fmt.Sprintf("%s: 不使用任何静态来源", PresetNone),
	}
	for _, group := range presetGroups {
		lines = append(lines, fmt.Sprintf("%s: %s（%d 个来源）", group.name, group.description, len(group.urls)))
	}
	return lines
}

// PresetURLs expands preset names into a de-duplicated, order-preserving URL
// list. Names are matched case-insensitively, and "all" expands to every group.
// An unknown name is an error rather than a silent skip, so a typo cannot
// quietly reduce coverage.
func PresetURLs(names []string) ([]string, error) {
	byName := make(map[string]presetGroup, len(presetGroups))
	for _, group := range presetGroups {
		byName[group.name] = group
	}

	expanded := make([]presetGroup, 0, len(names))
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		switch name {
		case "":
			continue
		case PresetNone:
			if len(names) > 1 {
				return nil, fmt.Errorf("publicsource: preset %q cannot be combined with other presets", PresetNone)
			}
			return nil, nil
		case PresetAll:
			expanded = append(expanded, presetGroups...)
		default:
			group, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("publicsource: unknown preset %q (available: %s)",
					raw, strings.Join(PresetNames(), ", "))
			}
			expanded = append(expanded, group)
		}
	}

	seen := make(map[string]struct{})
	out := make([]string, 0, 64)
	for _, group := range expanded {
		for _, url := range group.urls {
			if _, dup := seen[url]; dup {
				continue
			}
			seen[url] = struct{}{}
			out = append(out, url)
		}
	}
	return out, nil
}

// PresetURLsByGroup exposes each group's URLs for tests and documentation.
func PresetURLsByGroup() map[string][]string {
	out := make(map[string][]string, len(presetGroups))
	for _, group := range presetGroups {
		out[group.name] = append([]string(nil), group.urls...)
	}
	return out
}
