package assess

import "strings"

// hostingASNs is the built-in cloud/ hosting provider ASN list of §1.4. It is
// only consulted when no online source voted on the IP type, so it can never
// override real evidence; a match is always reported as ASN_HOSTING_HEURISTIC.
var hostingASNs = map[int]string{
	// Amazon Web Services.
	16509: "Amazon Web Services",
	14618: "Amazon Web Services",
	8987:  "Amazon Web Services",
	38895: "Amazon Web Services",
	// Google Cloud.
	15169:  "Google",
	396982: "Google Cloud",
	139070: "Google Cloud",
	// Microsoft Azure.
	8075:  "Microsoft",
	8068:  "Microsoft Azure",
	8069:  "Microsoft Azure",
	12076: "Microsoft Azure",
	20940: "Akamai",
	// Oracle Cloud.
	31898: "Oracle Cloud",
	14413: "Oracle Cloud",
	// Alibaba Cloud.
	45102: "Alibaba Cloud",
	37963: "Alibaba Cloud",
	45103: "Alibaba Cloud",
	// Tencent Cloud.
	132203: "Tencent Cloud",
	45090:  "Tencent Cloud",
	// Huawei Cloud.
	55990:  "Huawei Cloud",
	136907: "Huawei Cloud",
	23724:  "Huawei Cloud",
	// DigitalOcean.
	14061: "DigitalOcean",
	46652: "DigitalOcean",
	// Linode / Akamai.
	63949:  "Akamai (Linode)",
	16625:  "Akamai",
	209242: "Cloudflare",
	// Vultr (The Constant Company).
	20473: "Vultr",
	// Hetzner.
	24940:  "Hetzner",
	213230: "Hetzner",
	212317: "Hetzner",
	// OVH.
	16276: "OVH",
	35540: "OVH",
	// Cloudflare.
	13335: "Cloudflare",
}

// hostingASNName resolves one ASN to its provider name.
func hostingASNName(asn int) (string, bool) {
	if asn <= 0 {
		return "", false
	}
	name, ok := hostingASNs[asn]
	return name, ok
}

// HostingASN reports whether an ASN belongs to the built-in cloud provider list
// and returns the provider name.
func HostingASN(asn int) (string, bool) { return hostingASNName(asn) }

// HostingASNs returns a copy of the built-in cloud ASN table.
func HostingASNs() map[int]string {
	out := make(map[int]string, len(hostingASNs))
	for asn, name := range hostingASNs {
		out[asn] = name
	}
	return out
}

// IsOfflineSource reports whether a source id is one of the offline registries
// whose country/registration data feeds the §1.5 native heuristic.
func IsOfflineSource(source string) bool {
	return offlineSources[strings.ToLower(strings.TrimSpace(source))]
}
