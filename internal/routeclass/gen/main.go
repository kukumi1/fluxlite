// Command gen rebuilds prefixes_v4.txt: the IPv4 prefixes announced by the
// Chinese carriers' backbone and provincial networks, from RIPEstat.
//
//	go generate ./internal/routeclass
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"time"
)

// asns must cover every ASN routeclass.familyOf knows.
var asns = []int{
	4134, 4809, 23764, 4811, 4812, 4813, 4816, 23650, 23724,
	4837, 9929, 10099, 4808, 9800, 17621, 17622, 17623, 17816, 135061,
	9808, 58453, 58807, 24400, 56040, 56041, 56042, 56044, 56046, 56047, 56048,
}

func main() {
	out := flag.String("o", "prefixes_v4.txt", "output file")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Minute}

	type entry struct {
		prefix netip.Prefix
		asn    int
	}
	var entries []entry
	for _, asn := range asns {
		prefixes, err := announced(ctx, client, asn)
		if err != nil {
			log.Fatalf("AS%d: %v", asn, err)
		}
		for _, p := range prefixes {
			entries = append(entries, entry{p, asn})
		}
		time.Sleep(time.Second)
	}
	slices.SortStableFunc(entries, func(a, b entry) int {
		if c := a.prefix.Addr().Compare(b.prefix.Addr()); c != 0 {
			return c
		}
		return a.prefix.Bits() - b.prefix.Bits()
	})

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "# IPv4 prefixes announced by Chinese carrier networks, from RIPEstat announced-prefixes, %s.\n", time.Now().UTC().Format("2006-01-02"))
	fmt.Fprintf(w, "# Regenerate with: go generate ./internal/routeclass\n")
	// A prefix whose nearest covering prefix has the same origin adds nothing
	// to a longest-prefix lookup. Sorted by address then length, the covering
	// prefixes of an entry are exactly the stack below it.
	var stack []entry
	for _, e := range entries {
		for len(stack) > 0 && !stack[len(stack)-1].prefix.Contains(e.prefix.Addr()) {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 && stack[len(stack)-1].asn == e.asn {
			continue
		}
		stack = append(stack, e)
		fmt.Fprintf(w, "%s %d\n", e.prefix, e.asn)
	}
	if err := w.Flush(); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

func announced(ctx context.Context, client *http.Client, asn int) ([]netip.Prefix, error) {
	url := fmt.Sprintf("https://stat.ripe.net/data/announced-prefixes/data.json?resource=AS%d&sourceapp=fluxlite-routeclass", asn)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Prefixes []struct {
				Prefix string `json:"prefix"`
			} `json:"prefixes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, p := range body.Data.Prefixes {
		prefix, err := netip.ParsePrefix(p.Prefix)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		out = append(out, prefix.Masked())
	}
	return out, nil
}
