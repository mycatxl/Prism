// Command subprobe parses real subscription files with Prism's own parser and
// prints only structural statistics — never node credentials, addresses or
// passwords.
//
// Temporary diagnostic tool: not part of the shipped binary.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"prism/internal/subscription"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: subprobe <file>...")
		os.Exit(2)
	}
	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		nodes, err := subscription.ParseGeneralSubscription(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: parse: %v\n", path, err)
			continue
		}
		report(path, nodes)
	}
}

func report(path string, nodes []subscription.ParsedNode) {
	byProto := map[string][]map[string]any{}
	for _, parsed := range nodes {
		var obj map[string]any
		if json.Unmarshal(parsed.RawOptions, &obj) != nil {
			continue
		}
		proto, _ := obj["type"].(string)
		byProto[proto] = append(byProto[proto], obj)
	}

	fmt.Printf("=== %s: %d parsed nodes ===\n", path, len(nodes))
	protos := make([]string, 0, len(byProto))
	for proto := range byProto {
		protos = append(protos, proto)
	}
	sort.Strings(protos)
	for _, proto := range protos {
		fmt.Printf("  %-14s %d\n", proto, len(byProto[proto]))
	}

	for _, proto := range []string{"hysteria2", "hysteria", "trojan"} {
		group := byProto[proto]
		if len(group) == 0 {
			continue
		}
		counts := map[string]int{}
		for _, obj := range group {
			bump(counts, "server_port", hasKey(obj, "server_port"))
			bump(counts, "server_ports", hasKey(obj, "server_ports"))
			bump(counts, "up_mbps", hasKey(obj, "up_mbps"))
			bump(counts, "down_mbps", hasKey(obj, "down_mbps"))
			bump(counts, "obfs", hasKey(obj, "obfs"))
			bump(counts, "hop_interval", hasKey(obj, "hop_interval"))
			tls, _ := obj["tls"].(map[string]any)
			if tls == nil {
				continue
			}
			bump(counts, "tls.server_name", hasKey(tls, "server_name"))
			bump(counts, "tls.alpn", hasKey(tls, "alpn"))
			bump(counts, "tls.utls", hasKey(tls, "utls"))
			bump(counts, "tls.reality", hasKey(tls, "reality"))
			bump(counts, "tls.certificate", hasKey(tls, "certificate"))
			bump(counts, "tls.certificate_path", hasKey(tls, "certificate_path"))
			if v, ok := tls["insecure"]; ok && v == true {
				counts["tls.insecure=true"]++
			}
		}
		fmt.Printf("\n--- %s field coverage (%d nodes) ---\n", proto, len(group))
		keys := make([]string, 0, len(counts))
		for key := range counts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("  %-22s %d/%d\n", key, counts[key], len(group))
		}
	}
	fmt.Println()
}

func hasKey(m map[string]any, key string) bool {
	value, ok := m[key]
	return ok && value != nil
}

func bump(counts map[string]int, key string, present bool) {
	if present {
		counts[key]++
	}
}
