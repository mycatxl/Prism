package subscription

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
)

func TestRefreshInputsVersionMatchesConfiguration(t *testing.T) {
	s := NewSubscription("s", "source", "0", true, false)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 10000; i++ {
			s.SetFetchConfig(fmt.Sprint(i), 0)
		}
	}()
	for range 10000 {
		url, _, _, version := s.RefreshInputs()
		index, err := strconv.ParseInt(url, 10, 64)
		if err != nil || version != index+1 {
			t.Errorf("URL %q paired with version %d", url, version)
			break
		}
	}
	wg.Wait()
}
