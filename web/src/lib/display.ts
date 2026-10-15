// Keep stored slugs and pinned IDs intact; format names only at display sites.
// Common technical abbreviations stay uppercase ("dns-recovery" → "DNS recovery").
const acronyms = new Set([
  "ad", "api", "aws", "ccdc", "cdc", "cpu", "csv", "ctf", "dhcp", "dns", "ftp", "gpo",
  "http", "https", "ids", "iis", "ip", "ips", "ir", "ldap", "nfs", "os", "pki", "rdp",
  "siem", "smb", "smtp", "snmp", "sql", "ssh", "ssl", "tcp", "tls", "udp", "url", "vlan",
  "vm", "vpn", "waf", "xss",
]);

export function templateName(value: string) {
  const [name, ...context] = value.split(" · ");
  const words = name
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((word, i) => {
      const lower = word.toLowerCase();
      if (acronyms.has(lower)) return lower.toUpperCase();
      return i === 0 ? word.charAt(0).toUpperCase() + word.slice(1) : word;
    });
  return [words.join(" "), ...context].join(" · ");
}
