package subscription

import (
	"testing"
	"time"
)

func TestUsageHeaderOmitsUnlimitedAndPermanent(t *testing.T) {
	expire := time.Unix(1798761600, 0)
	cases := []struct {
		usage Usage
		want  string
	}{
		{Usage{Upload: 1, Download: 2, Total: 3, Expire: &expire}, "upload=1; download=2; total=3; expire=1798761600"},
		{Usage{Upload: 1, Download: 2}, "upload=1; download=2"},
		{Usage{Download: 5, Expire: &expire}, "upload=0; download=5; expire=1798761600"},
	}
	for _, c := range cases {
		if got := c.usage.Header(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestEmptyRendersEveryFormat(t *testing.T) {
	for _, format := range []string{"clash", "base64", "shadowrocket", "loon", "qx"} {
		r, err := Empty(format)
		if err != nil || r.IncludedCount != 0 {
			t.Fatalf("%s: %+v %v", format, r, err)
		}
	}
	if _, err := Empty("surge"); err != ErrFormat {
		t.Fatal(err)
	}
}
