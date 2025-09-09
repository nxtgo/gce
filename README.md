# gce

*formerly "gache"*

generic, fast, sharded, lock-aware in-memory cache for Go.

> [!IMPORTANT]
> `gce` requires **go >=1.25** as it uses the new `sync.WaitGroup` api.

## features

- shared concurrency
- optional **ttl**
- optional **lru eviction**
- atomic stats
- background stale entry cleanup
- simple

## usage

```go
package main

import (
	"fmt"
	"time"

	"github.com/nxtgo/gce"
)

func main() {
	// create cache with defaults
	c := gce.New[string, string]()

	// set values with TTL
	c.Set("foo", "bar", time.Minute)

	// get values
	if v, ok := c.Get("foo"); ok {
		fmt.Println("value:", v)
	}

	// stats
	fmt.Printf("%+v\n", c.Stats())

	// clean up
	c.Close()
}
```

## license

this project is released under CC0 1.0 public domain with
an additional IP waiver. do whatever you want with it.
no rights reserved.
