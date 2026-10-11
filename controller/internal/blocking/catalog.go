package blocking

// The list catalogue (the Blocking page's list picker): well-known lists
// by what they are for, each a source as a list definition takes it (kind:URL). Nothing is
// chosen for the user: the Blocking page offers these to tick, or a list by its address.
// It is data: the Blocking page's names and descriptions for a definition's sources come
// from here (CatalogFor), and anything else is shown by its address.

// CatalogList is one list of the catalogue.
type CatalogList struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Group  string `json:"group"` // what it is for (Groups)
	By     string `json:"by"`    // who keeps it
	What   string `json:"what"`  // what it blocks, in words
	Source string `json:"source"`
}

// Groups are the catalogue's purposes, in the order the picker shows them.
var Groups = []string{"Ads and tracking", "Malware and scams", "Adult content", "Gambling", "Social media",
	"Devices that phone home", "Piracy", "Other"}

const (
	hagezi = "wildcard:https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/"
	steven = "hosts:https://raw.githubusercontent.com/StevenBlack/hosts/master/"
	blp    = "hosts:https://blocklistproject.github.io/Lists/"
)

// Catalog is the catalogue.
var Catalog = []CatalogList{
	{"hagezi-light", "HaGeZi Light", "Ads and tracking", "HaGeZi", "Basic ads and tracking; almost never blocks something you need", hagezi + "light-onlydomains.txt"},
	{"hagezi-normal", "HaGeZi Normal", "Ads and tracking", "HaGeZi", "All-round ads, tracking and metrics", hagezi + "multi-onlydomains.txt"},
	{"hagezi-pro", "HaGeZi Pro", "Ads and tracking", "HaGeZi", "Broader; a good default for most homes", hagezi + "pro-onlydomains.txt"},
	{"hagezi-pro-plus", "HaGeZi Pro++", "Ads and tracking", "HaGeZi", "Stricter; now and then a site needs allowing", hagezi + "pro.plus-onlydomains.txt"},
	{"hagezi-ultimate", "HaGeZi Ultimate", "Ads and tracking", "HaGeZi", "Strictest; expect to allow some sites", hagezi + "ultimate-onlydomains.txt"},
	{"oisd-small", "OISD Small", "Ads and tracking", "oisd.nl", "Ads and tracking, very careful about false blocks", "wildcard:https://small.oisd.nl/domainswild2"},
	{"oisd-big", "OISD Big", "Ads and tracking", "oisd.nl", "Ads, tracking and malware, still careful", "wildcard:https://big.oisd.nl/domainswild2"},
	{"stevenblack", "StevenBlack hosts", "Ads and tracking", "Steven Black", "Ads and malware, merged from many sources", steven + "hosts"},
	{"adguard-dns", "AdGuard DNS filter", "Ads and tracking", "AdGuard", "AdGuard's own list made for DNS blocking", "adblock:https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt"},
	{"adaway", "AdAway", "Ads and tracking", "AdAway", "Mobile ads", "hosts:https://adaway.org/hosts.txt"},
	{"peter-lowe", "Peter Lowe's list", "Ads and tracking", "Peter Lowe", "Small, long-running ad and tracking list", "hosts:https://pgl.yoyo.org/adservers/serverlist.php?hostformat=hosts&showintro=0&mimetype=plaintext"},
	{"1hosts-lite", "1Hosts Lite", "Ads and tracking", "1Hosts", "Ads and tracking, light", "domains:https://badmojr.github.io/1Hosts/Lite/domains.txt"},
	{"developerdan-ads", "Developer Dan's Ads & Tracking", "Ads and tracking", "lightswitch05", "Ads and tracking", "hosts:https://www.github.developerdan.com/hosts/lists/ads-and-tracking-extended.txt"},
	{"frogeye-firstparty", "Frogeye first-party trackers", "Ads and tracking", "Frogeye", "Trackers hiding as a site's own subdomain", "hosts:https://hostfiles.frogeye.fr/firstparty-trackers-hosts.txt"},
	{"hagezi-tif", "HaGeZi Threat Intelligence", "Malware and scams", "HaGeZi", "Malware, phishing, scams and cryptojacking", hagezi + "tif-onlydomains.txt"},
	{"hagezi-fake", "HaGeZi Fake", "Malware and scams", "HaGeZi", "Fake shops, scams and fake news sites", hagezi + "fake-onlydomains.txt"},
	{"hagezi-popupads", "HaGeZi Pop-up Ads", "Malware and scams", "HaGeZi", "Malicious pop-up and push ads", hagezi + "popupads-onlydomains.txt"},
	{"urlhaus", "URLhaus", "Malware and scams", "abuse.ch", "Sites spreading malware", "hosts:https://urlhaus.abuse.ch/downloads/hostfile/"},
	{"phishing-army", "Phishing Army", "Malware and scams", "Phishing Army", "Phishing sites", "domains:https://phishing.army/download/phishing_army_blocklist.txt"},
	{"blp-ransomware", "Block List Project: Ransomware", "Malware and scams", "Block List Project", "Ransomware", blp + "ransomware.txt"},
	{"blp-scam", "Block List Project: Scam", "Malware and scams", "Block List Project", "Scam sites", blp + "scam.txt"},
	{"hagezi-nsfw", "HaGeZi NSFW", "Adult content", "HaGeZi", "Adult content", hagezi + "nsfw-onlydomains.txt"},
	{"oisd-nsfw", "OISD NSFW", "Adult content", "oisd.nl", "Adult content", "wildcard:https://nsfw.oisd.nl/domainswild2"},
	{"stevenblack-porn", "StevenBlack: porn", "Adult content", "Steven Black", "Adult content, as an add-on to StevenBlack hosts", steven + "alternates/porn-only/hosts"},
	{"hagezi-gambling", "HaGeZi Gambling", "Gambling", "HaGeZi", "Betting and casino sites", hagezi + "gambling-onlydomains.txt"},
	{"stevenblack-gambling", "StevenBlack: gambling", "Gambling", "Steven Black", "Gambling, as an add-on to StevenBlack hosts", steven + "alternates/gambling-only/hosts"},
	{"stevenblack-social", "StevenBlack: social", "Social media", "Steven Black", "Facebook, Instagram, TikTok, X and others", steven + "alternates/social-only/hosts"},
	{"blp-tiktok", "Block List Project: TikTok", "Social media", "Block List Project", "TikTok only", blp + "tiktok.txt"},
	{"blp-facebook", "Block List Project: Facebook", "Social media", "Block List Project", "Facebook and Instagram", blp + "facebook.txt"},
	{"hagezi-samsung", "HaGeZi: Samsung", "Devices that phone home", "HaGeZi", "Samsung phones and TVs reporting home", hagezi + "native.samsung-onlydomains.txt"},
	{"hagezi-amazon", "HaGeZi: Amazon", "Devices that phone home", "HaGeZi", "Echo, Fire TV and Kindle reporting home", hagezi + "native.amazon-onlydomains.txt"},
	{"hagezi-apple", "HaGeZi: Apple", "Devices that phone home", "HaGeZi", "Apple device telemetry", hagezi + "native.apple-onlydomains.txt"},
	{"hagezi-windows", "HaGeZi: Windows and Office", "Devices that phone home", "HaGeZi", "Windows and Office telemetry", hagezi + "native.winoffice-onlydomains.txt"},
	{"hagezi-xiaomi", "HaGeZi: Xiaomi", "Devices that phone home", "HaGeZi", "Xiaomi device telemetry", hagezi + "native.xiaomi-onlydomains.txt"},
	{"hagezi-lgwebos", "HaGeZi: LG webOS", "Devices that phone home", "HaGeZi", "LG TV telemetry", hagezi + "native.lgwebos-onlydomains.txt"},
	{"perflyst-smarttv", "Perflyst Smart TV", "Devices that phone home", "Perflyst", "Smart TV tracking", "domains:https://raw.githubusercontent.com/Perflyst/PiHoleBlocklist/master/SmartTV.txt"},
	{"hagezi-antipiracy", "HaGeZi Anti-Piracy", "Piracy", "HaGeZi", "Piracy and illegal streaming sites", hagezi + "anti.piracy-onlydomains.txt"},
	{"hagezi-dyndns", "HaGeZi DynDNS", "Other", "HaGeZi", "Dynamic DNS services often used by malware", hagezi + "dyndns-onlydomains.txt"},
	{"hagezi-hoster", "HaGeZi Badware Hoster", "Other", "HaGeZi", "Hosting services known for malware", hagezi + "hoster-onlydomains.txt"},
}

// CatalogFor is the catalogue's list with this source (as a definition holds it), if any.
func CatalogFor(spec string) (CatalogList, bool) {
	for _, c := range Catalog {
		if c.Source == spec {
			return c, true
		}
	}
	return CatalogList{}, false
}

// CatalogByID is the catalogue's list with this ID, if any.
func CatalogByID(id string) (CatalogList, bool) {
	for _, c := range Catalog {
		if c.ID == id {
			return c, true
		}
	}
	return CatalogList{}, false
}
