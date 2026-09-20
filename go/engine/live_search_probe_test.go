package engine

import (
	"context"
	"os"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func TestLiveSearchIfKeyed(t *testing.T) {
	if os.Getenv("NEOX_LIVE") == "" {
		t.Skip("set NEOX_LIVE=1 to hit the real search API")
	}
	s := SearcherFromEnv()
	if s == nil {
		t.Skip("NEOX_SEARCH_KEY 没配")
	}
	res, err := s.Search(context.Background(), abi.SearchParams{Query: "landlock linux ABI", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("真搜返回空 —— 适配或 key 有问题")
	}
	t.Logf("provider=%s hits=%d first=%s %s", res.Provider, len(res.Hits), res.Hits[0].Title, res.Hits[0].URL)
}
