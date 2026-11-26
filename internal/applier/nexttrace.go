package applier

// NextTraceVersion is the NextTrace release used to trace return routes. The
// tiny build is the traceroute alone, without the DNS client, web UI and
// geolocation front ends the panel never asks for.
const NextTraceVersion = "1.7.6"

var nextTraceSHA256 = map[string]string{
	"amd64": "9f219ba91318a3e1534bd28c661ef8019da42825ed48257035c508962d007271",
	"arm64": "aeab456e2f8aebd62cd1dd2d6458d2ec57b38f5d707e196bab41f999ff898810",
}

// CachedNextTraceSource serves the pinned NextTrace build for each machine.
type CachedNextTraceSource struct{ *pinnedBinary }

func NewCachedNextTraceSource(dir string) *CachedNextTraceSource {
	return &CachedNextTraceSource{newPinnedBinary("nexttrace", NextTraceVersion,
		"https://github.com/nxtrace/NTrace-core/releases/download/v"+NextTraceVersion+"/", dir,
		func(arch string) string { return "nexttrace-tiny_linux_" + arch }, nextTraceSHA256)}
}
