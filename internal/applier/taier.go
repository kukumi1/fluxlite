package applier

// TaierVersion is the taierspeedtest-mod release used for speed tests: a
// client of the China Academy of Information and Communications Technology
// speed test servers, measuring to each carrier in each province.
const TaierVersion = "1.0.7"

var taierSHA256 = map[string]string{
	"amd64": "720d19b688bfd3145fc0e0d5fa7866b9d9a319964f40647ca2bf3433d3db29e8",
	"arm64": "ba90c513cec19906c179db4738bc6ca1058783b8d18c2a46311b69558f8cf79d",
}

// CachedTaierSource serves the pinned taierspeedtest build for each machine.
type CachedTaierSource struct{ *pinnedBinary }

func NewCachedTaierSource(dir string) *CachedTaierSource {
	return &CachedTaierSource{newPinnedBinary("taierspeedtest", TaierVersion,
		"https://github.com/MbaoWang/taierspeedtest-mod/releases/download/v"+TaierVersion+"/", dir,
		func(arch string) string { return "taierspeedtest-linux-" + arch }, taierSHA256)}
}
